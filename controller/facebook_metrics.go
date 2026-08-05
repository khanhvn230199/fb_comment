package controller

import (
	"context"
	"errors"
	"log"
	"net/url"
	"strings"
	"time"

	"fb_comment/model"

	"gorm.io/gorm"
)

type FacebookMetricsUpdater struct {
	db     *gorm.DB
	logger *log.Logger
}

type facebookPostMetrics struct {
	TotalCommentCount int64
	TotalLikeCount    int64
}

func NewFacebookMetricsUpdater(db *gorm.DB, logger *log.Logger) *FacebookMetricsUpdater {
	if logger == nil {
		logger = log.Default()
	}
	return &FacebookMetricsUpdater{db: db, logger: logger}
}

func (u *FacebookMetricsUpdater) RefreshLinkMetricsForUser(ctx context.Context, link *model.Link, finalURL string, user model.User) error {
	if u == nil || u.db == nil || link == nil || user.ID == 0 {
		return nil
	}
	token, err := findFacebookTokenResource(user.ID, true)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return u.refreshLinkMetricsWithToken(ctx, link, finalURL, token)
}

func (u *FacebookMetricsUpdater) RefreshLinkMetrics(ctx context.Context, link *model.Link, finalURL string) error {
	if u == nil || u.db == nil || link == nil {
		return nil
	}
	token, err := u.tokenForLink(*link)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return u.refreshLinkMetricsWithToken(ctx, link, finalURL, token)
}

func (u *FacebookMetricsUpdater) tokenForLink(link model.Link) (model.Resource, error) {
	if link.UserID != nil {
		return findFacebookTokenResource(*link.UserID, true)
	}

	var admin model.User
	if err := u.db.Where("role = ?", model.RoleAdmin).Order("id ASC").First(&admin).Error; err == nil {
		if token, tokenErr := findFacebookTokenResource(admin.ID, true); tokenErr == nil || !errors.Is(tokenErr, gorm.ErrRecordNotFound) {
			return token, tokenErr
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Resource{}, err
	}

	var token model.Resource
	err := u.db.Where("type = ? AND status = ?", model.ResourceTypeToken, model.ResourceStatusActive).
		Order("last_used_at ASC NULLS FIRST, updated_at DESC, id DESC").
		First(&token).Error
	return token, err
}

func (u *FacebookMetricsUpdater) refreshLinkMetricsWithToken(ctx context.Context, link *model.Link, finalURL string, token model.Resource) error {
	candidates := facebookPostIDCandidates(finalURL, link.FinalURL, link.URL)
	if len(candidates) == 0 {
		return nil
	}

	for _, postID := range candidates {
		metrics, ok, err := fetchFacebookPostMetrics(ctx, postID, token)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		return u.updateLinkMetrics(link, metrics)
	}
	return nil
}

func fetchFacebookPostMetrics(ctx context.Context, postID string, token model.Resource) (facebookPostMetrics, bool, error) {
	graphURL, err := buildFacebookGraphURL(postID, 1, token.Value)
	if err != nil {
		return facebookPostMetrics{}, false, err
	}

	graphResponse, status, graphErr, err := fetchFacebookGraphPost(ctx, graphURL)
	if err != nil {
		return facebookPostMetrics{}, false, err
	}
	if graphErr != nil {
		message := facebookGraphErrorMessage(status, graphErr)
		updateFacebookTokenError(token, sanitizeFacebookError(message, token.Value), graphErr.Code == 190)
		if graphErr.Code == 190 {
			return facebookPostMetrics{}, false, nil
		}
		return facebookPostMetrics{}, false, nil
	}

	markFacebookTokenUsed(token)
	return facebookPostMetrics{
		TotalCommentCount: graphResponse.Comments.Summary.TotalCount,
		TotalLikeCount:    graphPostTotalLikeCount(graphResponse),
	}, true, nil
}

func (u *FacebookMetricsUpdater) updateLinkMetrics(link *model.Link, metrics facebookPostMetrics) error {
	now := time.Now().UTC()
	if err := u.db.Model(&model.Link{}).Where("id = ?", link.ID).Updates(map[string]any{
		"previous_comment_count": link.TotalCommentCount,
		"previous_like_count":    link.TotalLikeCount,
		"previous_metrics_at":    link.MetricsFetchedAt,
		"total_comment_count":    metrics.TotalCommentCount,
		"total_like_count":       metrics.TotalLikeCount,
		"metrics_fetched_at":     &now,
	}).Error; err != nil {
		return err
	}

	link.PreviousCommentCount = link.TotalCommentCount
	link.PreviousLikeCount = link.TotalLikeCount
	link.PreviousMetricsAt = link.MetricsFetchedAt
	link.TotalCommentCount = metrics.TotalCommentCount
	link.TotalLikeCount = metrics.TotalLikeCount
	link.MetricsFetchedAt = &now
	return nil
}

func facebookPostIDCandidates(rawValues ...string) []string {
	seen := map[string]struct{}{}
	candidates := []string{}
	for _, raw := range rawValues {
		for _, candidate := range facebookPostIDCandidatesFromURL(raw) {
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func facebookPostIDCandidatesFromURL(raw string) []string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil
	}

	seen := map[string]struct{}{}
	candidates := []string{}
	addCandidate := func(values ...string) {
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" || !isValidFacebookPostID(value) {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			candidates = append(candidates, value)
		}
	}
	addComposite := func(ownerID string, objectID string) {
		ownerID = strings.TrimSpace(ownerID)
		objectID = strings.TrimSpace(objectID)
		if ownerID != "" && objectID != "" {
			addCandidate(ownerID + "_" + objectID)
		}
		addCandidate(objectID)
	}

	query := parsed.Query()
	if storyID := strings.TrimSpace(query.Get("story_fbid")); storyID != "" {
		addComposite(query.Get("id"), storyID)
	}
	if fbID := strings.TrimSpace(query.Get("fbid")); fbID != "" {
		addComposite(query.Get("id"), fbID)
	}
	addCandidate(query.Get("v"))

	segments := facebookPathSegments(parsed.Path)
	for i, segment := range segments {
		switch segment {
		case "groups":
			if i+2 < len(segments) && (segments[i+2] == "posts" || segments[i+2] == "permalink") && i+3 < len(segments) {
				addComposite(segments[i+1], segments[i+3])
			}
		case "posts", "videos", "permalink", "reel", "reels", "watch":
			if i+1 < len(segments) {
				addCandidate(segments[i+1])
			}
		}
	}

	return candidates
}

func facebookPathSegments(pathValue string) []string {
	rawSegments := strings.Split(strings.Trim(pathValue, "/"), "/")
	segments := make([]string, 0, len(rawSegments))
	for _, segment := range rawSegments {
		segment, _ = url.PathUnescape(segment)
		segment = strings.TrimSpace(segment)
		if segment != "" {
			segments = append(segments, segment)
		}
	}
	return segments
}
