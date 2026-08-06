package controller

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fb_comment/model"
	"fb_comment/scraper"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultScrapeCommentLimit = 50
	maxScrapeCommentLimit     = 500
	defaultScrapeMaxScrolls   = 20
	defaultScrapeIdlePasses   = 2
	maxScrapeMaxScrolls       = 50
	maxScrapeIdlePasses       = 10
	maxScrapeLinksPerRequest  = 20
)

type ScrapeLinkInput struct {
	Title string `json:"title"`
	URL   string `json:"url" binding:"required"`
}

type ScrapeRequest struct {
	Links       []ScrapeLinkInput `json:"links" binding:"required"`
	MaxComments int               `json:"max_comments"`
	MaxScrolls  int               `json:"max_scrolls"`
	IdlePasses  int               `json:"idle_passes"`
	Refresh     bool              `json:"refresh"`
}

type ScrapeResponse struct {
	ScrapedAt time.Time      `json:"scraped_at"`
	Results   []ScrapeResult `json:"results"`
	Errors    []ScrapeError  `json:"errors"`
}

type ScrapeResult struct {
	InputURL          string          `json:"input_url"`
	Title             string          `json:"title"`
	URL               string          `json:"url"`
	FinalURL          string          `json:"final_url"`
	LinkID            uint            `json:"link_id"`
	LinkAction        string          `json:"link_action"`
	Status            string          `json:"status"`
	ScrapedCount      int             `json:"scraped_count"`
	InsertedCount     int64           `json:"inserted_count"`
	DuplicateCount    int             `json:"duplicate_count"`
	TotalCommentCount int64           `json:"total_comment_count"`
	TotalLikeCount    int64           `json:"total_like_count"`
	MetricsFetchedAt  *time.Time      `json:"metrics_fetched_at"`
	Comments          []model.Comment `json:"comments"`
}

type ScrapeError struct {
	URL   string `json:"url"`
	Error string `json:"error"`
}

func RegisterScrapeRoutes(router *gin.RouterGroup, commentScraper scraper.CommentScraper) {
	router.POST("/api/scrape", func(c *gin.Context) {
		ScrapeComments(c, commentScraper)
	})
}

func ScrapeComments(c *gin.Context, commentScraper scraper.CommentScraper) {
	if commentScraper == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Scraper chưa được khởi tạo"})
		return
	}

	var request ScrapeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dữ liệu scrape không hợp lệ"})
		return
	}

	if len(request.Links) > maxScrapeLinksPerRequest {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Mỗi request chỉ được cào tối đa 20 link"})
		return
	}

	links, invalidLinks := normalizeRequestLinks(request.Links)
	if len(links) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Không có link Facebook hợp lệ", "errors": invalidLinks})
		return
	}

	user := currentUser(c)
	maxComments := normalizeScrapeLimit(request.MaxComments)
	maxScrolls := normalizeScrapeScrolls(request.MaxScrolls)
	idlePasses := normalizeScrapeIdlePasses(request.IdlePasses)
	response := ScrapeResponse{ScrapedAt: time.Now().UTC()}

	for _, input := range links {
		link, action, err := findOrCreateScrapeLink(user, input, maxComments, maxScrolls, idlePasses)
		if err != nil {
			response.Errors = append(response.Errors, ScrapeError{URL: input.URL, Error: err.Error()})
			continue
		}

		scrapeCtx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
		scrapeResult, err := commentScraper.Scrape(scrapeCtx, scraper.CommentListRequest{
			URL:        link.URL,
			MaxScrolls: maxScrolls,
			IdlePasses: idlePasses,
			Refresh:    request.Refresh,
			Limit:      maxComments,
		})
		cancel()

		if err != nil {
			markLinkScrapeError(&link, err)
			response.Errors = append(response.Errors, ScrapeError{URL: input.URL, Error: err.Error()})
			response.Results = append(response.Results, ScrapeResult{
				InputURL:   input.URL,
				Title:      link.Title,
				URL:        link.URL,
				LinkID:     link.ID,
				LinkAction: action,
				Status:     "error",
			})
			continue
		}

		comments := mapScrapedComments(link.ID, scrapeResult.Comments, scrapeResult.ScrapedAt)
		inserted, err := insertComments(comments)
		if err != nil {
			markLinkScrapeError(&link, err)
			response.Errors = append(response.Errors, ScrapeError{URL: input.URL, Error: err.Error()})
			continue
		}

		markLinkScraped(&link, scrapeResult.FinalURL)
		response.Results = append(response.Results, ScrapeResult{
			InputURL:          input.URL,
			Title:             link.Title,
			URL:               link.URL,
			FinalURL:          scrapeResult.FinalURL,
			LinkID:            link.ID,
			LinkAction:        action,
			Status:            "scraped",
			ScrapedCount:      len(scrapeResult.Comments),
			InsertedCount:     inserted,
			DuplicateCount:    len(comments) - int(inserted),
			TotalCommentCount: link.TotalCommentCount,
			TotalLikeCount:    link.TotalLikeCount,
			MetricsFetchedAt:  link.MetricsFetchedAt,
			Comments:          comments,
		})
	}

	c.JSON(http.StatusOK, response)
}

func normalizeRequestLinks(rawLinks []ScrapeLinkInput) ([]ScrapeLinkInput, []ScrapeError) {
	seen := map[string]int{}
	links := make([]ScrapeLinkInput, 0, len(rawLinks))
	invalidLinks := []ScrapeError{}

	for _, rawLink := range rawLinks {
		rawURL := strings.TrimSpace(rawLink.URL)
		normalized, err := NormalizeFacebookURL(rawURL)
		if err != nil {
			invalidLinks = append(invalidLinks, ScrapeError{URL: rawURL, Error: err.Error()})
			continue
		}

		title := strings.TrimSpace(rawLink.Title)
		if existingIndex, exists := seen[normalized]; exists {
			if title != "" && links[existingIndex].Title == "" {
				links[existingIndex].Title = title
			}
			continue
		}

		seen[normalized] = len(links)
		links = append(links, ScrapeLinkInput{
			Title: title,
			URL:   normalized,
		})
	}

	return links, invalidLinks
}

func NormalizeFacebookURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("link rỗng")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" {
		parsed.Scheme = "https"
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("URL phải dùng http hoặc https")
	}

	host := strings.ToLower(parsed.Hostname())
	if host != "facebook.com" && host != "www.facebook.com" && host != "m.facebook.com" && host != "fb.watch" {
		return "", errors.New("URL phải là link Facebook")
	}

	parsed.Scheme = "https"
	parsed.Host = host
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.EscapedPath(), "/")
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String(), nil
}

func normalizeScrapeLimit(limit int) int {
	if limit <= 0 {
		return defaultScrapeCommentLimit
	}
	if limit > maxScrapeCommentLimit {
		return maxScrapeCommentLimit
	}
	if limit < defaultScrapeCommentLimit {
		return defaultScrapeCommentLimit
	}
	return limit
}

func normalizeScrapeScrolls(maxScrolls int) int {
	if maxScrolls <= 0 {
		return defaultScrapeMaxScrolls
	}
	if maxScrolls > maxScrapeMaxScrolls {
		return maxScrapeMaxScrolls
	}
	if maxScrolls < defaultScrapeMaxScrolls {
		return defaultScrapeMaxScrolls
	}
	return maxScrolls
}

func normalizeScrapeIdlePasses(idlePasses int) int {
	if idlePasses <= 0 {
		return defaultScrapeIdlePasses
	}
	if idlePasses > maxScrapeIdlePasses {
		return maxScrapeIdlePasses
	}
	if idlePasses < defaultScrapeIdlePasses {
		return defaultScrapeIdlePasses
	}
	return idlePasses
}

func findOrCreateScrapeLink(user model.User, input ScrapeLinkInput, maxComments int, maxScrolls int, idlePasses int) (model.Link, string, error) {
	var link model.Link
	err := model.DB.Where("url = ?", input.URL).First(&link).Error
	if err == nil {
		if !user.IsAdmin() && !canManageLink(user, link) {
			return model.Link{}, "", errors.New("Link đã tồn tại nhưng bạn không có quyền cập nhật")
		}
		action := "existing"
		if !link.Active {
			action = "reactivated"
		}
		prepareLinkForScrape(&link, input.Title, maxComments, maxScrolls, idlePasses)
		settings := model.LoadPollingSettings()
		model.ScheduleAllPolling(&link, settings, time.Now().UTC())
		return link, action, model.DB.Save(&link).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Link{}, "", err
	}

	link = model.NewLink(input.URL)
	link.Title = input.Title
	if !user.IsAdmin() {
		link.UserID = &user.ID
	}
	prepareLinkForScrape(&link, input.Title, maxComments, maxScrolls, idlePasses)
	settings := model.LoadPollingSettings()
	model.ScheduleAllPolling(&link, settings, time.Now().UTC())
	if err := model.DB.Create(&link).Error; err != nil {
		return model.Link{}, "", err
	}
	return link, "created", nil
}

func prepareLinkForScrape(link *model.Link, title string, maxComments int, maxScrolls int, idlePasses int) {
	if strings.TrimSpace(title) != "" {
		link.Title = strings.TrimSpace(title)
	}
	link.Active = true
	link.Status = "scraping"
	link.LastError = ""
	link.MaxComments = maxComments
	link.MaxScrolls = maxScrolls
	link.IdlePasses = idlePasses
	link.NextScrapeAt = time.Now()
	link.Normalize()
}

func mapScrapedComments(linkID uint, comments []scraper.APIComment, scrapedAt time.Time) []model.Comment {
	mapped := make([]model.Comment, 0, len(comments))
	seen := map[string]struct{}{}

	for _, item := range comments {
		commentText := strings.TrimSpace(item.Comment)
		if commentText == "" {
			continue
		}
		permalink := NormalizeCommentPermalink(item.Permalink)
		key := GenerateCommentKey(linkID, item.ProfileURL, item.User, item.Comment, item.RawText, permalink)
		if key == "" {
			key = NormalizeCommentKey(item.Key)
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
			Author:            strings.TrimSpace(item.User),
			AuthorUID:         model.ExtractFacebookUID(profileURL),
			Phone:             model.ExtractPhone(commentText, rawText),
			CommentText:       commentText,
			DateLabel:         strings.TrimSpace(item.Date),
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

func insertComments(comments []model.Comment) (int64, error) {
	if len(comments) == 0 {
		return 0, nil
	}

	comments, err := filterNewComments(comments)
	if err != nil {
		return 0, err
	}
	if len(comments) == 0 {
		return 0, nil
	}

	result := model.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&comments)
	return result.RowsAffected, result.Error
}

func filterNewComments(comments []model.Comment) ([]model.Comment, error) {
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
	query := model.DB.Model(&model.Comment{})
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

func markLinkScraped(link *model.Link, finalURL string) {
	now := time.Now().UTC()
	settings := model.LoadPollingSettings()
	link.FinalURL = strings.TrimSpace(finalURL)
	link.Status = "scraped"
	link.LastError = ""
	link.LastScrapedAt = &now
	model.ScheduleCommentCrawl(link, settings, now)
	_ = model.DB.Save(link).Error
}

func markLinkScrapeError(link *model.Link, scrapeErr error) {
	now := time.Now().UTC()
	settings := model.LoadPollingSettings()
	link.Status = "error"
	link.LastError = scrapeErr.Error()
	model.ScheduleCommentCrawl(link, settings, now)
	_ = model.DB.Save(link).Error
}
