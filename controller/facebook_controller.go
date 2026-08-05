package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"

	"fb_comment/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	defaultFacebookCommentLimit   = 10
	maxFacebookCommentLimit       = 10
	facebookGraphRequestTimeout   = 15 * time.Second
	facebookGraphErrorBodyMaxSize = 1 << 20
)

type FacebookTokenRequest struct {
	Name        string `json:"name"`
	AccessToken string `json:"access_token" binding:"required"`
}

type FacebookTokenResponse struct {
	ID          uint       `json:"id"`
	UserID      *uint      `json:"user_id"`
	Provider    string     `json:"provider"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	TokenMasked string     `json:"token_masked"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	LastError   string     `json:"last_error"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type FacebookGraphPostResponse struct {
	ID           string                   `json:"id"`
	Message      string                   `json:"message"`
	CreatedTime  string                   `json:"created_time"`
	PermalinkURL string                   `json:"permalink_url"`
	Comments     FacebookGraphComments    `json:"comments"`
	Reactions    FacebookGraphSummaryEdge `json:"reactions"`
	Likes        FacebookGraphSummaryEdge `json:"likes"`
	Error        *FacebookGraphError      `json:"error,omitempty"`
}

type FacebookGraphComments struct {
	Data    []FacebookGraphComment `json:"data"`
	Paging  FacebookGraphPaging    `json:"paging"`
	Summary FacebookGraphSummary   `json:"summary"`
}

type FacebookGraphSummary struct {
	TotalCount int64 `json:"total_count"`
}

type FacebookGraphSummaryEdge struct {
	Summary FacebookGraphSummary `json:"summary"`
}

type FacebookGraphComment struct {
	ID           string             `json:"id"`
	Message      string             `json:"message"`
	CreatedTime  string             `json:"created_time"`
	From         *FacebookGraphFrom `json:"from"`
	PermalinkURL string             `json:"permalink_url"`
	CommentCount int                `json:"comment_count"`
	LikeCount    int                `json:"like_count"`
}

type FacebookGraphFrom struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type FacebookGraphPaging struct {
	Cursors  FacebookGraphCursors `json:"cursors"`
	Next     string               `json:"next"`
	Previous string               `json:"previous"`
}

type FacebookGraphCursors struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

type FacebookGraphErrorResponse struct {
	Error FacebookGraphError `json:"error"`
}

type FacebookGraphError struct {
	Message      string `json:"message"`
	Type         string `json:"type"`
	Code         int    `json:"code"`
	ErrorSubcode int    `json:"error_subcode"`
	FBTraceID    string `json:"fbtrace_id"`
}

type FacebookPostCommentsResponse struct {
	PostID    string                         `json:"post_id"`
	FetchedAt time.Time                      `json:"fetched_at"`
	Comments  []FacebookPostCommentResponse  `json:"comments"`
	Paging    FacebookCommentsPagingResponse `json:"paging"`
	Post      FacebookPostMetadataResponse   `json:"post"`
}

type FacebookPostMetadataResponse struct {
	ID                string `json:"id"`
	Message           string `json:"message"`
	CreatedTime       string `json:"created_time"`
	PermalinkURL      string `json:"permalink_url"`
	TotalCommentCount int64  `json:"total_comment_count"`
	TotalLikeCount    int64  `json:"total_like_count"`
}

type FacebookPostCommentResponse struct {
	ID           string `json:"id"`
	Message      string `json:"message"`
	CreatedTime  string `json:"created_time"`
	AuthorID     string `json:"author_id"`
	AuthorName   string `json:"author_name"`
	PermalinkURL string `json:"permalink_url"`
	CommentCount int    `json:"comment_count"`
	LikeCount    int    `json:"like_count"`
}

type FacebookCommentsPagingResponse struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

func GetFacebookToken(c *gin.Context) {
	user := currentUser(c)
	token, err := findFacebookTokenResource(user.ID, true)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondFacebookError(c, http.StatusNotFound, "Bạn chưa cấu hình Facebook access token")
		return
	}
	if err != nil {
		respondFacebookError(c, http.StatusInternalServerError, "Không thể lấy Facebook access token")
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": facebookTokenResponse(token)})
}

func UpsertFacebookToken(c *gin.Context) {
	var request FacebookTokenRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		respondFacebookError(c, http.StatusBadRequest, "Dữ liệu Facebook access token không hợp lệ")
		return
	}

	accessToken := strings.TrimSpace(request.AccessToken)
	if accessToken == "" {
		respondFacebookError(c, http.StatusBadRequest, "Facebook access token là bắt buộc")
		return
	}
	if err := validateResourceValue(model.ResourceTypeToken, accessToken); err != nil {
		respondFacebookError(c, http.StatusBadRequest, err.Error())
		return
	}

	user := currentUser(c)
	token, err := findFacebookTokenResource(user.ID, false)
	status := http.StatusOK
	createdByID := user.ID
	if errors.Is(err, gorm.ErrRecordNotFound) {
		token = model.Resource{
			UserID:      &user.ID,
			Type:        model.ResourceTypeToken,
			CreatedByID: &createdByID,
		}
		status = http.StatusCreated
	} else if err != nil {
		respondFacebookError(c, http.StatusInternalServerError, "Không thể lưu Facebook access token")
		return
	}

	token.UserID = &user.ID
	token.Type = model.ResourceTypeToken
	token.Status = model.ResourceStatusActive
	token.Value = accessToken
	token.ValueHash = model.HashResourceValue(accessToken)
	token.LastError = ""
	token.Normalize()

	if err := model.DB.Save(&token).Error; err != nil {
		respondFacebookError(c, http.StatusInternalServerError, "Không thể lưu Facebook access token")
		return
	}

	c.JSON(status, gin.H{"token": facebookTokenResponse(token)})
}

func DeleteFacebookToken(c *gin.Context) {
	user := currentUser(c)
	token, err := findFacebookTokenResource(user.ID, false)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondFacebookError(c, http.StatusNotFound, "Bạn chưa cấu hình Facebook access token")
		return
	}
	if err != nil {
		respondFacebookError(c, http.StatusInternalServerError, "Không thể xóa Facebook access token")
		return
	}
	if err := model.DB.Delete(&token).Error; err != nil {
		respondFacebookError(c, http.StatusInternalServerError, "Không thể xóa Facebook access token")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Đã xóa Facebook access token"})
}

func FetchFacebookPostComments(c *gin.Context) {
	postID := strings.TrimSpace(c.Param("post_id"))
	if !isValidFacebookPostID(postID) {
		respondFacebookError(c, http.StatusBadRequest, "post_id không hợp lệ")
		return
	}

	user := currentUser(c)
	token, err := findFacebookTokenResource(user.ID, true)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondFacebookError(c, http.StatusNotFound, "Bạn chưa cấu hình Facebook access token")
		return
	}
	if err != nil {
		respondFacebookError(c, http.StatusInternalServerError, "Không thể lấy Facebook access token")
		return
	}

	limit := normalizeFacebookCommentLimit(queryIntWithDefault(c, "limit", defaultFacebookCommentLimit))
	graphURL, err := buildFacebookGraphURL(postID, limit, token.Value)
	if err != nil {
		respondFacebookError(c, http.StatusInternalServerError, "Không thể tạo Facebook Graph API request")
		return
	}

	graphResponse, status, graphErr, err := fetchFacebookGraphPost(c.Request.Context(), graphURL)
	if err != nil {
		message := "Không thể gọi Facebook Graph API"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			message = "Facebook Graph API timeout"
		}
		updateFacebookTokenError(token, message, false)
		respondFacebookError(c, http.StatusGatewayTimeout, message)
		return
	}

	if graphErr != nil {
		message := facebookGraphErrorMessage(status, graphErr)
		updateFacebookTokenError(token, sanitizeFacebookError(message, token.Value), graphErr.Code == 190)
		respondFacebookGraphError(c, status, message, graphErr)
		return
	}

	markFacebookTokenUsed(token)
	c.JSON(http.StatusOK, mapFacebookPostComments(postID, graphResponse))
}

func findFacebookTokenResource(userID uint, activeOnly bool) (model.Resource, error) {
	var token model.Resource
	query := model.DB.Where("user_id = ? AND type = ?", userID, model.ResourceTypeToken)
	if activeOnly {
		query = query.Where("status = ?", model.ResourceStatusActive)
	}
	return token, query.Order("last_used_at ASC NULLS FIRST, updated_at DESC, id DESC").First(&token).Error
}

func facebookTokenResponse(token model.Resource) FacebookTokenResponse {
	return FacebookTokenResponse{
		ID:          token.ID,
		UserID:      token.UserID,
		Provider:    model.AccessTokenProviderFacebook,
		Name:        model.DefaultAccessTokenName,
		Status:      token.Status,
		TokenMasked: model.MaskResourceValue(token.Value),
		LastUsedAt:  token.LastUsedAt,
		LastError:   token.LastError,
		CreatedAt:   token.CreatedAt,
		UpdatedAt:   token.UpdatedAt,
	}
}

func normalizeFacebookCommentLimit(limit int) int {
	if limit <= 0 {
		return defaultFacebookCommentLimit
	}
	if limit > maxFacebookCommentLimit {
		return maxFacebookCommentLimit
	}
	return limit
}

func isValidFacebookPostID(postID string) bool {
	if postID == "" || len(postID) > 200 {
		return false
	}
	for _, r := range postID {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func buildFacebookGraphURL(postID string, limit int, accessToken string) (string, error) {
	base, err := url.Parse(facebookGraphBaseURL())
	if err != nil {
		return "", err
	}
	if base.Scheme == "" || base.Host == "" {
		return "", errors.New("invalid Facebook Graph API base URL")
	}

	base.Path = strings.TrimRight(base.EscapedPath(), "/") + "/" + url.PathEscape(postID)
	fields := fmt.Sprintf(
		"id,message,created_time,permalink_url,comments.limit(%d).order(reverse_chronological).summary(true){id,message,created_time,from{id,name},permalink_url,comment_count,like_count},reactions.limit(0).summary(total_count),likes.limit(0).summary(true)",
		limit,
	)
	query := base.Query()
	query.Set("fields", fields)
	query.Set("access_token", accessToken)
	base.RawQuery = query.Encode()
	return base.String(), nil
}

func facebookGraphBaseURL() string {
	if value := strings.TrimSpace(os.Getenv("FACEBOOK_GRAPH_API_BASE")); value != "" {
		return value
	}
	return "https://graph.facebook.com"
}

func fetchFacebookGraphPost(ctx context.Context, graphURL string) (FacebookGraphPostResponse, int, *FacebookGraphError, error) {
	requestCtx, cancel := context.WithTimeout(ctx, facebookGraphRequestTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, graphURL, nil)
	if err != nil {
		return FacebookGraphPostResponse{}, http.StatusInternalServerError, nil, err
	}
	request.Header.Set("Accept", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return FacebookGraphPostResponse{}, http.StatusGatewayTimeout, nil, err
	}
	defer response.Body.Close()

	limitedBody := io.LimitReader(response.Body, facebookGraphErrorBodyMaxSize)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var graphError FacebookGraphErrorResponse
		if err := json.NewDecoder(limitedBody).Decode(&graphError); err != nil || graphError.Error.Message == "" {
			return FacebookGraphPostResponse{}, response.StatusCode, &FacebookGraphError{Message: "Không thể gọi Facebook Graph API"}, nil
		}
		return FacebookGraphPostResponse{}, response.StatusCode, &graphError.Error, nil
	}

	var graphResponse FacebookGraphPostResponse
	if err := json.NewDecoder(limitedBody).Decode(&graphResponse); err != nil {
		return FacebookGraphPostResponse{}, response.StatusCode, nil, err
	}
	if graphResponse.Error != nil {
		return FacebookGraphPostResponse{}, response.StatusCode, graphResponse.Error, nil
	}
	return graphResponse, response.StatusCode, nil, nil
}

func facebookGraphErrorMessage(status int, graphError *FacebookGraphError) string {
	if graphError == nil {
		return "Không thể gọi Facebook Graph API"
	}
	if graphError.Code == 190 {
		return "Facebook access token không hợp lệ hoặc đã hết hạn"
	}
	if graphError.Code == 100 || status == http.StatusNotFound {
		return "Không thể truy cập post Facebook này bằng token hiện tại"
	}
	if status == http.StatusForbidden {
		return "Không thể truy cập post Facebook này bằng token hiện tại"
	}
	return "Không thể gọi Facebook Graph API"
}

func respondFacebookGraphError(c *gin.Context, graphStatus int, message string, graphError *FacebookGraphError) {
	status := http.StatusBadGateway
	if graphError != nil {
		switch {
		case graphError.Code == 190:
			status = http.StatusUnauthorized
		case graphError.Code == 100 || graphStatus == http.StatusNotFound:
			status = http.StatusNotFound
		case graphStatus == http.StatusForbidden:
			status = http.StatusForbidden
		}
	}

	response := gin.H{"error": message}
	if graphError != nil && graphError.Code != 0 {
		response["facebook_error_code"] = graphError.Code
	}
	if graphError != nil && graphError.ErrorSubcode != 0 {
		response["facebook_error_subcode"] = graphError.ErrorSubcode
	}
	c.JSON(status, response)
}

func sanitizeFacebookError(message string, token string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "Không thể gọi Facebook Graph API"
	}
	if token != "" {
		message = strings.ReplaceAll(message, token, "[facebook_access_token]")
	}
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}

func updateFacebookTokenError(token model.Resource, message string, authError bool) {
	updates := map[string]any{"last_error": sanitizeFacebookError(message, token.Value)}
	if authError {
		updates["status"] = model.ResourceStatusError
	}
	_ = model.DB.Model(&token).Updates(updates).Error
}

func markFacebookTokenUsed(token model.Resource) {
	now := time.Now().UTC()
	_ = model.DB.Model(&token).Updates(map[string]any{
		"last_used_at": &now,
		"last_error":   "",
		"status":       model.ResourceStatusActive,
	}).Error
}

func graphPostTotalLikeCount(graphResponse FacebookGraphPostResponse) int64 {
	if graphResponse.Reactions.Summary.TotalCount > 0 {
		return graphResponse.Reactions.Summary.TotalCount
	}
	return graphResponse.Likes.Summary.TotalCount
}

func mapFacebookPostComments(postID string, graphResponse FacebookGraphPostResponse) FacebookPostCommentsResponse {
	comments := make([]FacebookPostCommentResponse, 0, len(graphResponse.Comments.Data))
	for _, comment := range graphResponse.Comments.Data {
		mapped := FacebookPostCommentResponse{
			ID:           comment.ID,
			Message:      comment.Message,
			CreatedTime:  comment.CreatedTime,
			PermalinkURL: comment.PermalinkURL,
			CommentCount: comment.CommentCount,
			LikeCount:    comment.LikeCount,
		}
		if comment.From != nil {
			mapped.AuthorID = comment.From.ID
			mapped.AuthorName = comment.From.Name
		}
		comments = append(comments, mapped)
	}

	return FacebookPostCommentsResponse{
		PostID:    postID,
		FetchedAt: time.Now().UTC(),
		Post: FacebookPostMetadataResponse{
			ID:                graphResponse.ID,
			Message:           graphResponse.Message,
			CreatedTime:       graphResponse.CreatedTime,
			PermalinkURL:      graphResponse.PermalinkURL,
			TotalCommentCount: graphResponse.Comments.Summary.TotalCount,
			TotalLikeCount:    graphPostTotalLikeCount(graphResponse),
		},
		Comments: comments,
		Paging: FacebookCommentsPagingResponse{
			Before: graphResponse.Comments.Paging.Cursors.Before,
			After:  graphResponse.Comments.Paging.Cursors.After,
		},
	}
}

func respondFacebookError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": message})
}
