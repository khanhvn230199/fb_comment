package poller

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fb_comment/model"
	"fb_comment/scraper"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultPollInterval = 1 * time.Second
	maxPollComments     = 500
)

type LinkPoller struct {
	db      *gorm.DB
	scraper scraper.CommentScraper
	logger  *log.Logger
}

func NewLinkPoller(db *gorm.DB, commentScraper scraper.CommentScraper, logger *log.Logger) *LinkPoller {
	if logger == nil {
		logger = log.Default()
	}
	return &LinkPoller{
		db:      db,
		scraper: commentScraper,
		logger:  logger,
	}
}

func (p *LinkPoller) Start(ctx context.Context) {
	ticker := time.NewTicker(defaultPollInterval)
	defer ticker.Stop()

	p.logger.Printf("link poller started: polling active links every %s", defaultPollInterval)
	p.pollOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			p.logger.Printf("link poller stopped")
			return
		case <-ticker.C:
			p.pollOnce(ctx)
		}
	}
}

func (p *LinkPoller) pollOnce(ctx context.Context) {
	if p.scraper == nil || p.db == nil {
		return
	}

	var links []model.Link
	now := time.Now().UTC()
	if err := p.db.Where("active = ? AND next_scrape_at <= ?", true, now).
		Order("next_scrape_at ASC, id ASC").
		Find(&links).Error; err != nil {
		p.logger.Printf("link poller: cannot list active links: %v", err)
		return
	}

	if len(links) == 0 {
		return
	}

	settings := model.LoadPollingSettings()
	p.logger.Printf("link poller: found %d due active link(s)", len(links))
	for i := range links {
		if err := ctx.Err(); err != nil {
			return
		}
		p.scrapeLink(ctx, &links[i], settings)
	}
}

func (p *LinkPoller) scrapeLink(ctx context.Context, link *model.Link, settings model.PollingSettings) {
	link.Status = "scraping"
	link.LastError = ""
	link.Normalize()
	if err := p.db.Save(link).Error; err != nil {
		p.logger.Printf("link poller: cannot mark link %d scraping: %v", link.ID, err)
		return
	}

	maxComments := normalizeCommentLimit(link.MaxComments)
	maxScrolls := normalizeMaxScrolls(link.MaxScrolls)
	scrapeCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	response, err := p.scraper.Scrape(scrapeCtx, scraper.CommentListRequest{
		URL:        link.URL,
		MaxScrolls: maxScrolls,
		IdlePasses: link.IdlePasses,
		Refresh:    true,
		Limit:      maxComments,
	})
	if err != nil {
		p.markError(link, settings, err)
		return
	}

	comments := mapScrapedComments(link.ID, response.Comments, response.ScrapedAt)
	inserted, err := p.insertComments(comments)
	if err != nil {
		p.markError(link, settings, err)
		return
	}

	p.markScraped(link, settings, response.FinalURL)
	if inserted > 0 {
		p.logger.Printf("link poller: link_id=%d inserted %d new comment(s)", link.ID, inserted)
	}
}

func (p *LinkPoller) insertComments(comments []model.Comment) (int64, error) {
	if len(comments) == 0 {
		return 0, nil
	}

	comments, err := p.filterNewComments(comments)
	if err != nil {
		return 0, err
	}
	if len(comments) == 0 {
		return 0, nil
	}

	result := p.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&comments)
	return result.RowsAffected, result.Error
}

func (p *LinkPoller) filterNewComments(comments []model.Comment) ([]model.Comment, error) {
	keys := make([]string, 0, len(comments))
	permalinks := make([]string, 0, len(comments))
	seenKey := map[string]struct{}{}
	seenPermalink := map[string]struct{}{}

	for _, comment := range comments {
		if comment.CommentKey != "" {
			if _, ok := seenKey[comment.CommentKey]; !ok {
				seenKey[comment.CommentKey] = struct{}{}
				keys = append(keys, comment.CommentKey)
			}
		}
		if comment.Permalink != "" {
			if _, ok := seenPermalink[comment.Permalink]; !ok {
				seenPermalink[comment.Permalink] = struct{}{}
				permalinks = append(permalinks, comment.Permalink)
			}
		}
	}

	existingKeys := map[string]struct{}{}
	existingPermalinks := map[string]struct{}{}
	query := p.db.Model(&model.Comment{})
	if len(keys) > 0 && len(permalinks) > 0 {
		query = query.Where("comment_key IN ? OR permalink IN ?", keys, permalinks)
	} else if len(keys) > 0 {
		query = query.Where("comment_key IN ?", keys)
	} else if len(permalinks) > 0 {
		query = query.Where("permalink IN ?", permalinks)
	} else {
		return comments, nil
	}

	var existing []model.Comment
	if err := query.Select("comment_key", "permalink").Find(&existing).Error; err != nil {
		return nil, err
	}
	for _, comment := range existing {
		if comment.CommentKey != "" {
			existingKeys[comment.CommentKey] = struct{}{}
		}
		if comment.Permalink != "" {
			existingPermalinks[comment.Permalink] = struct{}{}
		}
	}

	filtered := make([]model.Comment, 0, len(comments))
	batchKeys := map[string]struct{}{}
	batchPermalinks := map[string]struct{}{}
	for _, comment := range comments {
		if _, ok := existingKeys[comment.CommentKey]; ok {
			continue
		}
		if comment.Permalink != "" {
			if _, ok := existingPermalinks[comment.Permalink]; ok {
				continue
			}
			if _, ok := batchPermalinks[comment.Permalink]; ok {
				continue
			}
			batchPermalinks[comment.Permalink] = struct{}{}
		}
		if _, ok := batchKeys[comment.CommentKey]; ok {
			continue
		}
		batchKeys[comment.CommentKey] = struct{}{}
		filtered = append(filtered, comment)
	}

	return filtered, nil
}

func (p *LinkPoller) markScraped(link *model.Link, settings model.PollingSettings, finalURL string) {
	now := time.Now().UTC()
	link.FinalURL = strings.TrimSpace(finalURL)
	link.Status = "scraped"
	link.LastError = ""
	link.LastScrapedAt = &now
	model.ScheduleCommentCrawl(link, settings, now)
	if err := p.db.Save(link).Error; err != nil {
		p.logger.Printf("link poller: cannot update scraped link %d: %v", link.ID, err)
	}
}

func (p *LinkPoller) markError(link *model.Link, settings model.PollingSettings, scrapeErr error) {
	now := time.Now().UTC()
	link.Status = "error"
	link.LastError = scrapeErr.Error()
	model.ScheduleCommentCrawl(link, settings, now)
	if err := p.db.Save(link).Error; err != nil {
		p.logger.Printf("link poller: cannot update errored link %d: %v", link.ID, err)
	}
}

func mapScrapedComments(linkID uint, comments []scraper.APIComment, scrapedAt time.Time) []model.Comment {
	mapped := make([]model.Comment, 0, len(comments))
	seen := map[string]struct{}{}

	for _, item := range comments {
		commentText := strings.TrimSpace(item.Comment)
		if commentText == "" {
			continue
		}

		permalink := normalizeCommentPermalink(item.Permalink)
		key := generateCommentKey(linkID, item.ProfileURL, item.User, item.Comment, item.RawText, permalink)
		if key == "" {
			key = normalizeCommentKey(item.Key)
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}

		profileURL := strings.TrimSpace(item.ProfileURL)
		rawText := strings.TrimSpace(item.RawText)
		mapped = append(mapped, model.Comment{
			LinkID:            linkID,
			CommentKey:        key,
			Author:            trimMax(item.User, 255),
			AuthorUID:         model.ExtractFacebookUID(profileURL),
			Phone:             model.ExtractPhone(commentText, rawText),
			CommentText:       commentText,
			DateLabel:         trimMax(item.Date, 100),
			RawText:           rawText,
			FacebookCreatedAt: model.ResolveFacebookCommentTime(scrapedAt, item.Date),
			ProfileURL:        profileURL,
			Permalink:         permalink,
			FirstSeenAt:       scrapedAt,
			ScrapedAt:         scrapedAt,
		})
	}

	return mapped
}

func normalizeCommentLimit(limit int) int {
	if limit <= 0 || limit > maxPollComments {
		return maxPollComments
	}
	if limit < 50 {
		return 50
	}
	return limit
}

func normalizeMaxScrolls(maxScrolls int) int {
	if maxScrolls <= 0 {
		return 20
	}
	if maxScrolls > 50 {
		return 50
	}
	if maxScrolls < 20 {
		return 20
	}
	return maxScrolls
}

func generateCommentKey(linkID uint, profileURL, author, commentText, rawText, permalink string) string {
	if commentID := extractCommentIDFromPermalink(permalink); commentID != "" {
		return commentID
	}
	if permalink = normalizeCommentPermalink(permalink); permalink != "" {
		return "permalink:" + sha1Hex(permalink)
	}

	identity := strings.Join(
		[]string{
			strconv.FormatUint(uint64(linkID), 10),
			strings.TrimSpace(profileURL),
			strings.TrimSpace(author),
			strings.TrimSpace(commentText),
		},
		"\n",
	)
	if strings.TrimSpace(strings.ReplaceAll(identity, "\n", "")) != "" {
		return "identity:" + sha1Hex(identity)
	}

	return "raw:" + sha1Hex(strconv.FormatUint(uint64(linkID), 10)+"\n"+strings.TrimSpace(rawText))
}

func extractCommentIDFromPermalink(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	query := parsed.Query()
	if commentID := strings.TrimSpace(query.Get("comment_id")); commentID != "" {
		return commentID
	}
	return strings.TrimSpace(query.Get("reply_comment_id"))
}

func normalizeCommentPermalink(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.Scheme == "" {
		parsed.Scheme = "https"
	}
	parsed.Scheme = "https"
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.EscapedPath(), "/")
	if parsed.Path == "" {
		parsed.Path = "/"
	}

	query := parsed.Query()
	cleanQuery := url.Values{}
	for _, key := range []string{"comment_id", "reply_comment_id"} {
		if value := strings.TrimSpace(query.Get(key)); value != "" {
			cleanQuery.Set(key, value)
		}
	}
	parsed.RawQuery = cleanQuery.Encode()

	return parsed.String()
}

func normalizeCommentKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if commentID := extractCommentIDFromPermalink(raw); commentID != "" {
		return commentID
	}

	if key, ok := strings.CutPrefix(raw, "permalink:"); ok {
		return "permalink:" + sha1Hex(key)
	}
	if key, ok := strings.CutPrefix(raw, "identity:"); ok {
		if len(key) == 40 {
			return raw
		}
		return "identity:" + sha1Hex(key)
	}
	if key, ok := strings.CutPrefix(raw, "raw:"); ok {
		if len(key) == 40 {
			return raw
		}
		return "raw:" + sha1Hex(key)
	}
	if len(raw) > 120 {
		return "external:" + sha1Hex(raw)
	}

	return raw
}

func sha1Hex(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func trimMax(value string, max int) string {
	value = strings.TrimSpace(value)
	if len([]rune(value)) <= max {
		return value
	}
	return string([]rune(value)[:max])
}
