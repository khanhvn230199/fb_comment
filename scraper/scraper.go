package scraper

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/mxschmitt/playwright-go"
)

type ScraperConfig struct {
	URL         string
	Headless    bool
	Watch       bool
	NewOnly     bool
	Serve       bool
	LoginMode   bool
	PollMS      int
	WaitMS      int
	MaxScrolls  int
	IdlePasses  int
	BatchSize   int
	TimeoutMS   int
	OutputPath  string
	StoragePath string
	OldestFirst bool
	Addr        string
}

func ConfigFromEnv() ScraperConfig {
	return ScraperConfig{
		Headless:    envBool("SCRAPER_HEADLESS", true),
		WaitMS:      envInt("SCRAPER_WAIT_MS", 1000),
		MaxScrolls:  envInt("SCRAPER_MAX_SCROLLS", 1),
		IdlePasses:  envInt("SCRAPER_IDLE_PASSES", 2),
		TimeoutMS:   envInt("SCRAPER_TIMEOUT_MS", 60000),
		StoragePath: os.Getenv("SCRAPER_STORAGE_PATH"),
	}
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

type FacebookPost struct {
	URL     string   `json:"url"`
	Author  string   `json:"author,omitempty"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

type FacebookComment struct {
	Key         string    `json:"key"`
	Author      string    `json:"author,omitempty"`
	Content     string    `json:"content"`
	Date        string    `json:"date,omitempty"`
	RawText     string    `json:"rawText,omitempty"`
	ProfileURL  string    `json:"profileUrl,omitempty"`
	Permalink   string    `json:"permalink,omitempty"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
}

type CommentEvent struct {
	Type      string          `json:"type"`
	ScrapedAt time.Time       `json:"scrapedAt"`
	PostURL   string          `json:"postUrl"`
	FinalURL  string          `json:"finalUrl"`
	Comment   FacebookComment `json:"comment"`
}

type CommentBatchEvent struct {
	Type      string            `json:"type"`
	ScrapedAt time.Time         `json:"scrapedAt"`
	PostURL   string            `json:"postUrl"`
	FinalURL  string            `json:"finalUrl"`
	Count     int               `json:"count"`
	Comments  []FacebookComment `json:"comments"`
}

type CommentListRequest struct {
	URL         string `json:"url"`
	MaxScrolls  int    `json:"maxScrolls,omitempty"`
	IdlePasses  int    `json:"idlePasses,omitempty"`
	OldestFirst bool   `json:"oldestFirst,omitempty"`
	Refresh     bool   `json:"refresh,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

type APIComment struct {
	Comment    string `json:"comment"`
	User       string `json:"user"`
	Date       string `json:"date,omitempty"`
	ProfileURL string `json:"profileUrl,omitempty"`
	Permalink  string `json:"permalink,omitempty"`
	Key        string `json:"key,omitempty"`
	RawText    string `json:"rawText,omitempty"`
}

type CommentListResponse struct {
	ScrapedAt         time.Time    `json:"scrapedAt"`
	URL               string       `json:"url"`
	FinalURL          string       `json:"finalUrl"`
	Count             int          `json:"count"`
	TotalCommentCount *int64       `json:"totalCommentCount,omitempty"`
	TotalLikeCount    *int64       `json:"totalLikeCount,omitempty"`
	Comments          []APIComment `json:"comments"`
}

type PostMetrics struct {
	TotalCommentCount *int64
	TotalLikeCount    *int64
}

func (m PostMetrics) HasAny() bool {
	return m.TotalCommentCount != nil || m.TotalLikeCount != nil
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type FacebookScraper struct {
	cfg         ScraperConfig
	pw          *playwright.Playwright
	browser     playwright.Browser
	context     playwright.BrowserContext
	page        playwright.Page
	ownsBrowser bool
	seen        map[string]struct{}
	writer      *bufio.Writer
	outputFile  *os.File
}

type APIScraper struct {
	cfg     ScraperConfig
	pw      *playwright.Playwright
	browser playwright.Browser
	mu      sync.Mutex
	cache   map[string]cachedCommentResponse
}

type CommentScraper interface {
	Scrape(ctx context.Context, req CommentListRequest) (CommentListResponse, error)
}

type cachedCommentResponse struct {
	response  CommentListResponse
	expiresAt time.Time
}

var commentTimestampSuffixPattern = regexp.MustCompile(
	`(?i)(?:vừa xong|just now|\d+\s*(?:giây|phút|giờ|ngày|tuần|tháng|năm|seconds?|secs?|minutes?|mins?|hours?|hrs?|days?|weeks?|months?|years?|s|min|m|h|d|w|y))$`,
)

var commentActionSuffixPattern = regexp.MustCompile(
	`(?i)(?:thích|like|phản hồi|reply|chia sẻ|share|đã chỉnh sửa|edited|xem bản dịch|see translation)$`,
)

var socialCountPattern = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)([kmb])?(?:\s+(nghìn|ngàn|triệu|tỷ))?`)

func RunScraper(ctx context.Context, cfg ScraperConfig) error {
	scraper := NewFacebookScraper(cfg)
	defer scraper.Close()

	if err := scraper.InitOutput(); err != nil {
		return err
	}

	if err := scraper.Start(); err != nil {
		return err
	}

	if err := scraper.OpenPost(); err != nil {
		return err
	}

	if err := scraper.PrepareComments(ctx); err != nil {
		log.Printf("could not prepare comments completely: %v", err)
	}

	if !cfg.Watch {
		return scraper.ScanAndEmitNewComments()
	}

	return scraper.Watch(ctx)
}

func NewAPIScraper(cfg ScraperConfig) *APIScraper {
	return &APIScraper{
		cfg:   cfg,
		cache: map[string]cachedCommentResponse{},
	}
}

func (s *APIScraper) Start() error {
	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("could not start playwright: %w", err)
	}

	s.pw = pw

	browser, err := pw.Chromium.Launch(
		playwright.BrowserTypeLaunchOptions{
			Headless: playwright.Bool(s.cfg.Headless),
		},
	)
	if err != nil {
		_ = pw.Stop()
		return fmt.Errorf("could not launch chromium: %w", err)
	}

	s.browser = browser
	return nil
}

func (s *APIScraper) Close() {
	if s.browser != nil {
		_ = s.browser.Close()
	}

	if s.pw != nil {
		_ = s.pw.Stop()
	}
}

func (s *APIScraper) Scrape(
	ctx context.Context,
	req CommentListRequest,
) (CommentListResponse, error) {
	cacheKey := commentCacheKey(req)
	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !req.Refresh {
		if cached, ok := s.cache[cacheKey]; ok && now.Before(cached.expiresAt) {
			return cached.response, nil
		}
	}

	response, err := s.scrapeFresh(ctx, req)
	if err != nil {
		return CommentListResponse{}, err
	}

	s.cache[cacheKey] = cachedCommentResponse{
		response:  response,
		expiresAt: time.Now().UTC().Add(15 * time.Second),
	}

	return response, nil
}

func (s *APIScraper) scrapeFresh(
	ctx context.Context,
	req CommentListRequest,
) (CommentListResponse, error) {
	cfg := s.cfg
	cfg.URL = req.URL
	cfg.Watch = false
	cfg.NewOnly = false
	cfg.OutputPath = ""
	cfg.MaxScrolls = req.MaxScrolls
	cfg.IdlePasses = req.IdlePasses
	cfg.OldestFirst = req.OldestFirst

	if err := ctx.Err(); err != nil {
		return CommentListResponse{}, err
	}

	scraper := NewFacebookScraper(cfg)
	defer scraper.Close()

	if err := scraper.StartWithBrowser(s.browser); err != nil {
		return CommentListResponse{}, err
	}

	if err := scraper.OpenPost(); err != nil {
		return CommentListResponse{}, err
	}

	if err := scraper.PrepareComments(ctx); err != nil {
		log.Printf("API could not prepare comments completely: %v", err)
	}

	metrics, err := scraper.ExtractPostMetrics()
	if err != nil {
		log.Printf("API could not extract post metrics completely: %v", err)
	}

	comments, err := scraper.ExtractComments()
	if err != nil {
		return CommentListResponse{}, err
	}

	comments = scraper.orderComments(comments)
	comments = dedupeComments(comments)
	comments = limitComments(comments, req.Limit)
	apiComments := toAPIComments(comments)

	return CommentListResponse{
		ScrapedAt:         time.Now().UTC(),
		URL:               req.URL,
		FinalURL:          scraper.page.URL(),
		Count:             len(apiComments),
		TotalCommentCount: metrics.TotalCommentCount,
		TotalLikeCount:    metrics.TotalLikeCount,
		Comments:          apiComments,
	}, nil
}

func commentCacheKey(req CommentListRequest) string {
	return strings.Join(
		[]string{
			req.URL,
			strconv.Itoa(req.MaxScrolls),
			strconv.Itoa(req.IdlePasses),
			strconv.FormatBool(req.OldestFirst),
			strconv.Itoa(normalizeLimit(req.Limit)),
		},
		"\n",
	)
}

func normalizeLimit(limit int) int {
	if limit <= 0 || limit > 500 {
		return 100
	}
	return limit
}

func normalizeIdlePasses(idlePasses int) int {
	if idlePasses <= 0 {
		return 2
	}
	if idlePasses > 10 {
		return 10
	}
	return idlePasses
}

func limitComments(comments []FacebookComment, limit int) []FacebookComment {
	limit = normalizeLimit(limit)
	if len(comments) <= limit {
		return comments
	}
	return comments[:limit]
}

func toAPIComments(comments []FacebookComment) []APIComment {
	apiComments := make([]APIComment, 0, len(comments))

	for _, comment := range comments {
		apiComments = append(
			apiComments,
			APIComment{
				Comment:    comment.Content,
				User:       comment.Author,
				Date:       comment.Date,
				ProfileURL: comment.ProfileURL,
				Permalink:  comment.Permalink,
				Key:        comment.Key,
				RawText:    comment.RawText,
			},
		)
	}

	return apiComments
}

func dedupeComments(comments []FacebookComment) []FacebookComment {
	seen := map[string]struct{}{}
	unique := make([]FacebookComment, 0, len(comments))

	for _, comment := range comments {
		if comment.Key == "" {
			continue
		}

		if _, ok := seen[comment.Key]; ok {
			continue
		}

		seen[comment.Key] = struct{}{}
		unique = append(unique, comment)
	}

	return unique
}

func RunLogin(cfg ScraperConfig) error {
	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("could not start playwright: %w", err)
	}
	defer pw.Stop()

	browser, err := pw.Chromium.Launch(
		playwright.BrowserTypeLaunchOptions{
			Headless: playwright.Bool(false),
		},
	)
	if err != nil {
		return fmt.Errorf("could not launch chromium: %w", err)
	}
	defer browser.Close()

	browserContext, err := browser.NewContext(
		playwright.BrowserNewContextOptions{
			Locale: playwright.String("vi-VN"),
		},
	)
	if err != nil {
		return fmt.Errorf("could not create browser context: %w", err)
	}
	defer browserContext.Close()

	page, err := browserContext.NewPage()
	if err != nil {
		return fmt.Errorf("could not create page: %w", err)
	}

	_, err = page.Goto(
		"https://www.facebook.com/",
		playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateDomcontentloaded,
			Timeout:   playwright.Float(float64(cfg.TimeoutMS)),
		},
	)
	if err != nil {
		return fmt.Errorf("could not open Facebook login page: %w", err)
	}

	fmt.Fprintln(
		os.Stderr,
		"Đăng nhập Facebook trong cửa sổ Chromium, sau đó quay lại terminal và nhấn Enter để lưu session.",
	)

	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')

	if cfg.StoragePath == "" {
		return errors.New("-storage must not be empty in login mode")
	}

	_, err = browserContext.StorageState(
		playwright.BrowserContextStorageStateOptions{
			Path: playwright.String(cfg.StoragePath),
		},
	)
	if err != nil {
		return fmt.Errorf("could not save storage state: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Đã lưu session vào %s\n", cfg.StoragePath)
	return nil
}

func NewFacebookScraper(cfg ScraperConfig) *FacebookScraper {
	return &FacebookScraper{
		cfg:  cfg,
		seen: map[string]struct{}{},
	}
}

func (s *FacebookScraper) InitOutput() error {
	if s.cfg.OutputPath != "" {
		if err := s.LoadSeenFromOutput(); err != nil {
			return err
		}

		if err := os.MkdirAll(
			filepath.Dir(s.cfg.OutputPath),
			0o755,
		); err != nil {
			return fmt.Errorf("could not create output directory: %w", err)
		}

		file, err := os.OpenFile(
			s.cfg.OutputPath,
			os.O_CREATE|os.O_WRONLY|os.O_APPEND,
			0o644,
		)
		if err != nil {
			return fmt.Errorf("could not open output file: %w", err)
		}

		s.outputFile = file
		s.writer = bufio.NewWriter(file)
	} else {
		s.writer = bufio.NewWriter(os.Stdout)
	}

	return nil
}

func (s *FacebookScraper) LoadSeenFromOutput() error {
	if s.cfg.OutputPath == "" || !fileExists(s.cfg.OutputPath) {
		return nil
	}

	file, err := os.Open(s.cfg.OutputPath)
	if err != nil {
		return fmt.Errorf("could not read existing output file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1024*1024)

	for scanner.Scan() {
		var event CommentEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}

		if event.Comment.Key != "" {
			s.seen[event.Comment.Key] = struct{}{}
		}

		var batch CommentBatchEvent
		if err := json.Unmarshal(scanner.Bytes(), &batch); err == nil {
			for _, comment := range batch.Comments {
				if comment.Key != "" {
					s.seen[comment.Key] = struct{}{}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("could not scan existing output file: %w", err)
	}

	return nil
}

func (s *FacebookScraper) Start() error {
	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("could not start playwright: %w", err)
	}

	s.pw = pw

	browser, err := pw.Chromium.Launch(
		playwright.BrowserTypeLaunchOptions{
			Headless: playwright.Bool(s.cfg.Headless),
		},
	)
	if err != nil {
		return fmt.Errorf("could not launch chromium: %w", err)
	}

	s.ownsBrowser = true
	return s.StartWithBrowser(browser)
}

func (s *FacebookScraper) StartWithBrowser(browser playwright.Browser) error {
	s.browser = browser

	options := playwright.BrowserNewContextOptions{
		Locale: playwright.String("vi-VN"),
	}

	if s.cfg.StoragePath != "" && fileExists(s.cfg.StoragePath) {
		options.StorageStatePath = playwright.String(s.cfg.StoragePath)
	}

	browserContext, err := browser.NewContext(options)
	if err != nil {
		return fmt.Errorf("could not create browser context: %w", err)
	}

	s.context = browserContext

	page, err := browserContext.NewPage()
	if err != nil {
		return fmt.Errorf("could not create page: %w", err)
	}

	s.page = page
	return nil
}

func (s *FacebookScraper) OpenPost() error {
	_, err := s.page.Goto(
		s.cfg.URL,
		playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateDomcontentloaded,
			Timeout:   playwright.Float(float64(s.cfg.TimeoutMS)),
		},
	)
	if err != nil {
		return fmt.Errorf("could not open facebook post: %w", err)
	}

	article := s.page.Locator(`[role="article"]`).First()
	err = article.WaitFor(
		playwright.LocatorWaitForOptions{
			Timeout: playwright.Float(20_000),
		},
	)
	if err != nil {
		return fmt.Errorf(
			"could not find facebook post; post may not be public, may require login, or Facebook DOM changed. Try: go run . -login -headful",
		)
	}

	expandPost(article)
	return nil
}

func (s *FacebookScraper) PrepareComments(ctx context.Context) error {
	trySelectNewestComments(s.page)

	maxPasses := s.cfg.MaxScrolls
	if maxPasses < 20 {
		maxPasses = 20
	}
	if maxPasses > 50 {
		maxPasses = 50
	}

	idleLimit := normalizeIdlePasses(s.cfg.IdlePasses)
	stablePasses := 0
	seen := map[string]struct{}{}

	for pass := 0; pass < maxPasses; pass++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := refreshVisibleComments(
			s.page,
			s.cfg.WaitMS,
		); err != nil {
			return err
		}

		comments, err := s.ExtractComments()
		if err != nil {
			return err
		}

		newCount := 0
		for _, comment := range comments {
			if comment.Key == "" {
				continue
			}
			if _, ok := seen[comment.Key]; ok {
				continue
			}
			seen[comment.Key] = struct{}{}
			newCount++
		}

		log.Printf("comment scrape pass %d: +%d new comment(s), seen=%d, idle=%d/%d", pass+1, newCount, len(seen), stablePasses, idleLimit)

		if newCount == 0 {
			stablePasses++
			if stablePasses >= idleLimit {
				log.Printf("comment scrape stopped after %d idle pass(es)", stablePasses)
				break
			}
			continue
		}

		stablePasses = 0
	}

	return nil
}

func (s *FacebookScraper) Watch(ctx context.Context) error {
	if s.cfg.NewOnly {
		count, err := s.MarkCurrentCommentsSeen()
		if err != nil {
			log.Printf("initial comment baseline failed: %v", err)
		} else {
			log.Printf("baseline: marked %d currently visible comments as seen; only future comments will be emitted", count)
		}
	} else if err := s.ScanAndEmitNewComments(); err != nil {
		log.Printf("initial comment scan failed: %v", err)
	}

	ticker := time.NewTicker(
		time.Duration(s.cfg.PollMS) * time.Millisecond,
	)
	defer ticker.Stop()

	failures := 0

	for {
		select {
		case <-ctx.Done():
			return nil

		case <-ticker.C:
			if err := refreshVisibleComments(
				s.page,
				s.cfg.WaitMS,
			); err != nil {
				log.Printf("could not refresh comments: %v", err)
			}

			if err := s.ScanAndEmitNewComments(); err != nil {
				failures++
				log.Printf("comment scan failed: %v", err)

				if failures >= 3 {
					log.Print("too many scan failures; reloading page once")
					_, _ = s.page.Reload()
					failures = 0
				}

				continue
			}

			failures = 0
		}
	}
}

func (s *FacebookScraper) MarkCurrentCommentsSeen() (int, error) {
	comments, err := s.ExtractComments()
	if err != nil {
		return 0, err
	}

	added := 0
	for _, comment := range comments {
		if comment.Key == "" {
			continue
		}

		if _, ok := s.seen[comment.Key]; ok {
			continue
		}

		s.seen[comment.Key] = struct{}{}
		added++
	}

	return added, nil
}

func (s *FacebookScraper) ScanAndEmitNewComments() error {
	comments, err := s.ExtractComments()
	if err != nil {
		return err
	}

	comments = s.orderComments(comments)
	pending := make([]FacebookComment, 0, len(comments))
	queued := map[string]struct{}{}

	for _, comment := range comments {
		if comment.Key == "" {
			continue
		}

		if _, ok := s.seen[comment.Key]; ok {
			continue
		}

		if _, ok := queued[comment.Key]; ok {
			continue
		}

		queued[comment.Key] = struct{}{}
		pending = append(pending, comment)
	}

	for start := 0; start < len(pending); start += s.cfg.BatchSize {
		end := start + s.cfg.BatchSize
		if end > len(pending) {
			end = len(pending)
		}

		batch := pending[start:end]
		if err := s.EmitComments(batch); err != nil {
			return err
		}

		for _, comment := range batch {
			s.seen[comment.Key] = struct{}{}
		}
	}

	return nil
}

func (s *FacebookScraper) orderComments(comments []FacebookComment) []FacebookComment {
	if !s.cfg.OldestFirst {
		return comments
	}

	ordered := make([]FacebookComment, len(comments))
	for i, comment := range comments {
		ordered[len(comments)-1-i] = comment
	}

	return ordered
}

func (s *FacebookScraper) EmitComments(comments []FacebookComment) error {
	if len(comments) == 0 {
		return nil
	}

	if s.writer == nil {
		return errors.New("output writer is not initialized")
	}

	now := time.Now().UTC()
	var event any

	if len(comments) == 1 && s.cfg.BatchSize == 1 {
		event = CommentEvent{
			Type:      "comment",
			ScrapedAt: now,
			PostURL:   s.cfg.URL,
			FinalURL:  s.page.URL(),
			Comment:   comments[0],
		}
	} else {
		event = CommentBatchEvent{
			Type:      "comments",
			ScrapedAt: now,
			PostURL:   s.cfg.URL,
			FinalURL:  s.page.URL(),
			Count:     len(comments),
			Comments:  comments,
		}
	}

	if err := copyJSONLine(s.writer, event); err != nil {
		return fmt.Errorf("could not encode comment event: %w", err)
	}

	if err := s.writer.Flush(); err != nil {
		return fmt.Errorf("could not flush comment event: %w", err)
	}

	return nil
}

func (s *FacebookScraper) ExtractPostMetrics() (PostMetrics, error) {
	result, err := s.page.Evaluate(`
		() => {
			const clean = value => {
				if (!value) return "";
				return String(value)
					.replace(/ /g, " ")
					.replace(/[ \t]+/g, " ")
					.trim();
			};

			const labels = new Set();
			const add = value => {
				const text = clean(value);
				if (!text) return;
				text.split('\n').map(clean).filter(Boolean).forEach(line => labels.add(line));
			};

			const root = document.querySelector('[role="main"]') || document.body;
			if (!root) return [];
			add(root.innerText);
			root.querySelectorAll('[aria-label], [title], span, a, div').forEach(el => {
				add(el.getAttribute('aria-label') || '');
				add(el.getAttribute('title') || '');
				const text = clean(el.innerText || el.textContent || '');
				if (text.length <= 120) add(text);
			});

			return Array.from(labels).slice(0, 800);
		}
	`, nil)
	if err != nil {
		return PostMetrics{}, fmt.Errorf("could not extract post metrics: %w", err)
	}

	data, err := json.Marshal(result)
	if err != nil {
		return PostMetrics{}, err
	}

	var labels []string
	if err := json.Unmarshal(data, &labels); err != nil {
		return PostMetrics{}, err
	}

	metrics := PostMetrics{}
	for _, label := range labels {
		if metrics.TotalCommentCount == nil && isCommentMetricLabel(label) {
			if value, ok := parseSocialCount(label); ok {
				metrics.TotalCommentCount = &value
			}
		}
		if metrics.TotalLikeCount == nil && isLikeMetricLabel(label) {
			if value, ok := parseSocialCount(label); ok {
				metrics.TotalLikeCount = &value
			}
		}
		if metrics.TotalCommentCount != nil && metrics.TotalLikeCount != nil {
			break
		}
	}

	return metrics, nil
}

func (s *FacebookScraper) ExtractComments() ([]FacebookComment, error) {
	result, err := s.page.Evaluate(`
		() => {
			const clean = value => {
				if (!value) {
					return "";
				}

				return value
					.replace(/ /g, " ")
					.replace(/[ \t]+/g, " ")
					.replace(/\n{3,}/g, "\n\n")
					.trim();
			};

			const timestampPattern = /(?:Vừa xong|Just now|\d+\s*(?:giây|phút|giờ|ngày|tuần|tháng|năm|seconds?|secs?|minutes?|mins?|hours?|hrs?|days?|weeks?|months?|years?|s|min|m|h|d|w|y))$/i;

			const timestampLine = value => {
				const line = clean(value);
				return /^(Vừa xong|Just now)$/i.test(line) ||
					/^\d+\s*(giây|phút|giờ|ngày|tuần|tháng|năm|seconds?|secs?|minutes?|mins?|hours?|hrs?|days?|weeks?|months?|years?|s|min|m|h|d|w|y)$/i.test(line) ||
					/^\d+[smhdwy]$/i.test(line);
			};

			const actionLine = value => {
				const line = clean(value);
				if (!line) {
					return true;
				}

				return /^(Thích|Like|Phản hồi|Reply|Chia sẻ|Share|Đã chỉnh sửa|Edited|Ẩn|Hide|Xem bản dịch|See translation)$/i.test(line) ||
					timestampLine(line);
			};

			const absoluteURL = href => {
				try {
					return new URL(href, location.href).href;
				} catch (_) {
					return "";
				}
			};

			const articles = Array.from(
				document.querySelectorAll('div[role="article"]')
			);

			return articles.slice(1).map(el => {
				const rawText = clean(el.innerText);
				if (!rawText || rawText.length < 2) {
					return null;
				}

				const links = Array.from(
					el.querySelectorAll('a[href]')
				);

				let author = "";
				let profileUrl = "";
				let permalink = "";
				let date = "";

				for (const link of links) {
					const text = clean(link.innerText);
					const href = absoluteURL(
						link.getAttribute('href') || ""
					);

					if (
						!author &&
						text &&
						text.length <= 100 &&
						!actionLine(text)
					) {
						author = text;
						profileUrl = href;
					}

					if (
						!permalink &&
						/comment_id=|reply_comment_id=|comment_tracking=|\/comments\//i.test(href)
					) {
						permalink = href;
					}
				}

				const lines = rawText
					.split('\n')
					.map(clean)
					.filter(Boolean);

				for (const line of lines) {
					if (timestampLine(line)) {
						date = line;
					}
				}

				if (!date) {
					const gluedDate = rawText.match(timestampPattern);
					if (gluedDate) {
						date = clean(gluedDate[0]);
					}
				}

				const contentLines = lines.filter(line => {
					if (actionLine(line)) {
						return false;
					}

					if (author && line === author) {
						return false;
					}

					return !/^(Tác giả|Author|Top fan|Người đóng góp nhiều nhất)$/i.test(line);
				});

				const content = clean(contentLines.join('\n'));
				if (!content || content.length < 2) {
					return null;
				}

				return {
					author,
					content,
					date,
					rawText,
					profileUrl,
					permalink
				};
			}).filter(Boolean);
		}
	`, nil)
	if err != nil {
		return nil, fmt.Errorf("could not extract comments: %w", err)
	}

	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}

	var comments []FacebookComment
	if err := json.Unmarshal(data, &comments); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	filtered := make([]FacebookComment, 0, len(comments))

	for _, comment := range comments {
		comment.Author = normalizeText(comment.Author)
		comment.Date = normalizeText(comment.Date)
		comment.Content = cleanCommentContent(
			comment.Author,
			comment.Content,
			comment.Date,
		)
		comment.RawText = normalizeText(comment.RawText)

		if comment.Date == "" {
			comment.Date = extractCommentDate(comment.RawText)
			comment.Content = cleanCommentContent(
				comment.Author,
				comment.Content,
				comment.Date,
			)
		}
		comment.ProfileURL = strings.TrimSpace(comment.ProfileURL)
		comment.Permalink = strings.TrimSpace(comment.Permalink)

		if comment.Content == "" {
			continue
		}

		comment.FirstSeenAt = now
		comment.Key = commentKey(comment)
		filtered = append(filtered, comment)
	}

	return filtered, nil
}

func (s *FacebookScraper) Close() {
	if s.writer != nil {
		_ = s.writer.Flush()
	}

	if s.outputFile != nil {
		_ = s.outputFile.Close()
	}

	if s.context != nil {
		_ = s.context.Close()
	}

	if s.browser != nil && s.ownsBrowser {
		_ = s.browser.Close()
	}

	if s.pw != nil {
		_ = s.pw.Stop()
	}
}

func refreshVisibleComments(
	page playwright.Page,
	waitMS int,
) error {
	commentButtonSelectors := []string{
		`div[role="button"]:has-text("Xem thêm bình luận")`,
		`span[role="button"]:has-text("Xem thêm bình luận")`,
		`div[role="button"]:has-text("View more comments")`,
		`span[role="button"]:has-text("View more comments")`,
		`div[role="button"]:has-text("View previous comments")`,
		`span[role="button"]:has-text("View previous comments")`,
		`div[role="button"]:has-text("Xem thêm phản hồi")`,
		`span[role="button"]:has-text("Xem thêm phản hồi")`,
		`div[role="button"]:has-text("View more replies")`,
		`span[role="button"]:has-text("View more replies")`,
		`div[role="button"]:has-text("View replies")`,
		`span[role="button"]:has-text("View replies")`,
	}

	clickVisibleButtons(page, commentButtonSelectors, 3)

	_, err := page.Evaluate(`
		() => {
			window.scrollBy({
				top: Math.max(500, window.innerHeight * 0.7),
				left: 0,
				behavior: "instant"
			});
		}
	`, nil)
	if err != nil {
		return fmt.Errorf("could not scroll comments: %w", err)
	}

	sleepMillis(waitMS)
	return nil
}

func trySelectNewestComments(page playwright.Page) {
	sortButtons := []string{
		`div[role="button"]:has-text("Phù hợp nhất")`,
		`span[role="button"]:has-text("Phù hợp nhất")`,
		`div[role="button"]:has-text("Most relevant")`,
		`span[role="button"]:has-text("Most relevant")`,
		`div[role="button"]:has-text("Tất cả bình luận")`,
		`span[role="button"]:has-text("Tất cả bình luận")`,
		`div[role="button"]:has-text("All comments")`,
		`span[role="button"]:has-text("All comments")`,
	}

	if !clickVisibleButtons(page, sortButtons, 1) {
		return
	}

	sleepMillis(500)

	newestItems := []string{
		`div[role="menuitem"]:has-text("Mới nhất")`,
		`span:has-text("Mới nhất")`,
		`div[role="menuitem"]:has-text("Newest")`,
		`span:has-text("Newest")`,
		`div[role="menuitem"]:has-text("Tất cả bình luận")`,
		`span:has-text("Tất cả bình luận")`,
		`div[role="menuitem"]:has-text("All comments")`,
		`span:has-text("All comments")`,
	}

	clickVisibleButtons(page, newestItems, 1)
	sleepMillis(500)
}

func clickVisibleButtons(
	page playwright.Page,
	selectors []string,
	maxClicks int,
) bool {
	if maxClicks <= 0 {
		return false
	}

	clicked := false

	for _, selector := range selectors {
		for range maxClicks {
			button := page.Locator(selector).First()
			count, err := button.Count()
			if err != nil || count == 0 {
				break
			}

			err = button.Click(
				playwright.LocatorClickOptions{
					Timeout: playwright.Float(1_500),
				},
			)
			if err != nil {
				break
			}

			clicked = true
			sleepMillis(300)
		}
	}

	return clicked
}

func expandPost(article playwright.Locator) {
	selectors := []string{
		`div[role="button"]:has-text("Xem thêm")`,
		`span[role="button"]:has-text("Xem thêm")`,
		`div[role="button"]:has-text("See more")`,
		`span[role="button"]:has-text("See more")`,
	}

	for _, selector := range selectors {
		button := article.Locator(selector).First()

		count, err := button.Count()
		if err != nil || count == 0 {
			continue
		}

		_ = button.Click(
			playwright.LocatorClickOptions{
				Timeout: playwright.Float(2_000),
			},
		)

		return
	}
}

func cleanCommentContent(
	author string,
	content string,
	date string,
) string {
	content = normalizeText(content)
	author = normalizeText(author)
	date = normalizeText(date)

	if author != "" {
		content = strings.TrimSpace(strings.TrimPrefix(content, author))
	}

	if date != "" {
		content = strings.TrimSpace(strings.TrimSuffix(content, date))
	}

	content = strings.TrimSpace(
		commentTimestampSuffixPattern.ReplaceAllString(content, ""),
	)
	content = strings.TrimSpace(
		commentActionSuffixPattern.ReplaceAllString(content, ""),
	)

	return normalizeText(content)
}

func isCommentMetricLabel(value string) bool {
	value = strings.ToLower(normalizeText(value))
	return strings.Contains(value, "comment") || strings.Contains(value, "bình luận")
}

func isLikeMetricLabel(value string) bool {
	value = strings.ToLower(normalizeText(value))
	if strings.Contains(value, "bình luận") || strings.Contains(value, "comment") || strings.Contains(value, "share") || strings.Contains(value, "chia sẻ") {
		return false
	}
	return strings.Contains(value, "like") || strings.Contains(value, "thích") || strings.Contains(value, "reaction") || strings.Contains(value, "cảm xúc")
}

func parseSocialCount(value string) (int64, bool) {
	match := socialCountPattern.FindStringSubmatch(normalizeText(value))
	if len(match) == 0 {
		return 0, false
	}

	number, err := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", "."), 64)
	if err != nil {
		return 0, false
	}

	suffix := strings.ToLower(strings.TrimSpace(match[2]))
	if suffix == "" {
		suffix = strings.ToLower(strings.TrimSpace(match[3]))
	}

	multiplier := float64(1)
	switch suffix {
	case "k", "nghìn", "ngàn":
		multiplier = 1_000
	case "m", "triệu":
		multiplier = 1_000_000
	case "b", "tỷ":
		multiplier = 1_000_000_000
	}

	return int64(math.Round(number * multiplier)), true
}

func extractCommentDate(rawText string) string {
	return normalizeText(
		commentTimestampSuffixPattern.FindString(
			normalizeText(rawText),
		),
	)
}

func normalizeText(value string) string {
	value = strings.Map(
		func(r rune) rune {
			if r == ' ' {
				return ' '
			}

			if unicode.IsSpace(r) {
				return ' '
			}

			return r
		},
		value,
	)

	return strings.Join(strings.Fields(value), " ")
}

func commentKey(comment FacebookComment) string {
	if value := normalizeText(comment.Permalink); value != "" {
		return "permalink:" + value
	}

	identity := strings.Join(
		[]string{
			normalizeText(comment.ProfileURL),
			normalizeText(comment.Author),
			normalizeText(comment.Content),
		},
		"\n",
	)
	if normalizeText(identity) != "" {
		return "identity:" + hashString(identity)
	}

	return "raw:" + hashString(comment.RawText)
}

func hashString(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func ValidateFacebookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("URL must use http or https")
	}

	host := strings.ToLower(u.Hostname())

	switch host {
	case "facebook.com",
		"www.facebook.com",
		"m.facebook.com":
		return nil

	default:
		return errors.New("URL must belong to facebook.com")
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func copyJSONLine(
	writer io.Writer,
	event any,
) error {
	return json.NewEncoder(writer).Encode(event)
}

func sleepMillis(milliseconds int) {
	time.Sleep(time.Duration(milliseconds) * time.Millisecond)
}
