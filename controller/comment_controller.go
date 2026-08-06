package controller

import (
	"crypto/sha1"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fb_comment/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CommentRequest struct {
	LinkID            uint   `json:"link_id" form:"link_id"`
	CommentKey        string `json:"comment_key" form:"comment_key"`
	Author            string `json:"author" form:"author"`
	AuthorUID         string `json:"author_uid" form:"author_uid"`
	Phone             string `json:"phone" form:"phone"`
	CommentText       string `json:"comment_text" form:"comment_text"`
	DateLabel         string `json:"date_label" form:"date_label"`
	RawText           string `json:"raw_text" form:"raw_text"`
	ProfileURL        string `json:"profile_url" form:"profile_url"`
	Permalink         string `json:"permalink" form:"permalink"`
	FacebookCreatedAt string `json:"facebook_created_at" form:"facebook_created_at"`
}

const commentDateLayout = "2006-01-02"

type CommentFilters struct {
	LinkID      uint
	StartDate   string
	EndDate     string
	HasDate     bool
	DateStart   time.Time
	DateEnd     time.Time
	DefaultDate bool
}

func commentDateRangeFromValues(startValue, endValue, legacyValue string, defaultToday bool) (CommentFilters, int, string) {
	filters := CommentFilters{}
	startValue = strings.TrimSpace(startValue)
	endValue = strings.TrimSpace(endValue)
	legacyValue = strings.TrimSpace(legacyValue)

	if startValue == "" && endValue == "" {
		if legacyValue != "" {
			startValue = legacyValue
			endValue = legacyValue
		} else if defaultToday {
			today := time.Now().In(time.Local).Format(commentDateLayout)
			startValue = today
			endValue = today
			filters.DefaultDate = true
		} else {
			return filters, 0, ""
		}
	} else {
		if startValue == "" {
			startValue = endValue
		}
		if endValue == "" {
			endValue = startValue
		}
	}

	startDay, err := time.ParseInLocation(commentDateLayout, startValue, time.Local)
	if err != nil {
		return filters, http.StatusBadRequest, "Ngày comment không hợp lệ"
	}
	endDay, err := time.ParseInLocation(commentDateLayout, endValue, time.Local)
	if err != nil {
		return filters, http.StatusBadRequest, "Ngày comment không hợp lệ"
	}
	if startDay.After(endDay) {
		return filters, http.StatusBadRequest, "Ngày bắt đầu phải trước hoặc bằng ngày kết thúc"
	}

	filters.StartDate = startValue
	filters.EndDate = endValue
	filters.HasDate = true
	filters.DateStart = startDay.UTC()
	filters.DateEnd = endDay.AddDate(0, 0, 1).UTC()
	return filters, 0, ""
}

func commentFiltersFromRequest(c *gin.Context, user model.User, defaultToday bool) (CommentFilters, int, string) {
	filters := CommentFilters{}
	if linkID := queryIntWithDefault(c, "link_id", 0); linkID > 0 {
		ok, err := canAccessLink(user, uint(linkID))
		if err != nil {
			return filters, http.StatusInternalServerError, "Không thể kiểm tra quyền link"
		}
		if !ok {
			return filters, http.StatusNotFound, "Không tìm thấy link"
		}
		filters.LinkID = uint(linkID)
	}

	dateFilters, status, message := commentDateRangeFromValues(c.Query("comment_start_date"), c.Query("comment_end_date"), c.Query("comment_date"), defaultToday)
	if status != 0 {
		return filters, status, message
	}
	if dateFilters.HasDate {
		filters.StartDate = dateFilters.StartDate
		filters.EndDate = dateFilters.EndDate
		filters.HasDate = true
		filters.DateStart = dateFilters.DateStart
		filters.DateEnd = dateFilters.DateEnd
		filters.DefaultDate = dateFilters.DefaultDate
	}

	return filters, 0, ""
}

func applyCommentFilters(query *gorm.DB, filters CommentFilters) *gorm.DB {
	if filters.LinkID > 0 {
		query = query.Where("comments.link_id = ?", filters.LinkID)
	}
	if filters.HasDate {
		query = query.Where(
			"((comments.facebook_created_at IS NOT NULL AND comments.facebook_created_at >= ? AND comments.facebook_created_at < ?) OR (comments.facebook_created_at IS NULL AND comments.scraped_at >= ? AND comments.scraped_at < ?))",
			filters.DateStart,
			filters.DateEnd,
			filters.DateStart,
			filters.DateEnd,
		)
	}
	return query
}

func commentsExportURL(c *gin.Context) string {
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		values = url.Values{}
	}
	values.Del("page")
	values.Del("offset")
	values.Del("limit")
	values.Del("per_page")
	encoded := values.Encode()
	if encoded == "" {
		return "/comments/export"
	}
	return "/comments/export?" + encoded
}

func ShowCommentsPage(c *gin.Context) {
	user := currentUser(c)
	pagination := paginationFromRequest(c, 50)
	filters, status, message := commentFiltersFromRequest(c, user, true)
	if status != 0 {
		c.HTML(status, "comments.html", gin.H{"currentUser": user, "error": message})
		return
	}
	query := applyCommentFilters(scopedCommentsQuery(user), filters)

	var total int64
	if err := query.Count(&total).Error; err != nil {
		c.HTML(http.StatusInternalServerError, "comments.html", gin.H{"currentUser": user, "error": "Không thể đếm danh sách comment"})
		return
	}
	pagination = completePagination(pagination, total)

	var comments []model.Comment
	if err := query.Preload("Link").Order("COALESCE(comments.facebook_created_at, comments.scraped_at) DESC, comments.scraped_at DESC, comments.id ASC").Limit(pagination.Limit).Offset(pagination.Offset).Find(&comments).Error; err != nil {
		c.HTML(http.StatusInternalServerError, "comments.html", gin.H{"currentUser": user, "error": "Không thể lấy danh sách comment"})
		return
	}

	normalizeCommentTimes(comments)

	var links []model.Link
	if err := scopedLinksQuery(user).Order("links.created_at DESC").Limit(maxPaginationPerPage).Find(&links).Error; err != nil {
		c.HTML(http.StatusInternalServerError, "comments.html", gin.H{"currentUser": user, "error": "Không thể lấy danh sách link"})
		return
	}

	c.HTML(http.StatusOK, "comments.html", gin.H{
		"comments":               comments,
		"links":                  links,
		"currentUser":            user,
		"limit":                  pagination.PerPage,
		"pagination":             pagination,
		"prevPageURL":            paginationPrevURL(c, pagination),
		"nextPageURL":            paginationNextURL(c, pagination),
		"filterCommentStartDate": filters.StartDate,
		"filterCommentEndDate":   filters.EndDate,
		"filterLinkID":           strconv.FormatUint(uint64(filters.LinkID), 10),
		"exportURL":              commentsExportURL(c),
		"error":                  c.Query("error"),
		"success":                c.Query("success"),
	})
}

func ListComments(c *gin.Context) {
	user := currentUser(c)
	pagination := paginationFromRequest(c, 50)
	filters, status, message := commentFiltersFromRequest(c, user, false)
	if status != 0 {
		c.JSON(status, gin.H{"error": message})
		return
	}
	query := applyCommentFilters(scopedCommentsQuery(user), filters)

	var total int64
	if err := query.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể đếm danh sách comment"})
		return
	}
	pagination = completePagination(pagination, total)

	var comments []model.Comment
	if err := query.Preload("Link").Order("COALESCE(comments.facebook_created_at, comments.scraped_at) DESC, comments.scraped_at DESC, comments.id ASC").Limit(pagination.Limit).Offset(pagination.Offset).Find(&comments).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể lấy danh sách comment"})
		return
	}

	normalizeCommentTimes(comments)

	c.JSON(http.StatusOK, gin.H{"comments": comments, "pagination": pagination})
}

func ExportComments(c *gin.Context) {
	user := currentUser(c)
	filters, status, message := commentFiltersFromRequest(c, user, true)
	if status != 0 {
		c.String(status, message)
		return
	}

	var comments []model.Comment
	query := applyCommentFilters(scopedCommentsQuery(user), filters)
	if err := query.Preload("Link").Order("COALESCE(comments.facebook_created_at, comments.scraped_at) DESC, comments.scraped_at DESC, comments.id ASC").Limit(50000).Find(&comments).Error; err != nil {
		c.String(http.StatusInternalServerError, "Không thể export comment")
		return
	}

	normalizeCommentTimes(comments)

	filenameDate := filters.StartDate
	if filenameDate == "" {
		filenameDate = time.Now().In(time.Local).Format(commentDateLayout)
	}
	if filters.HasDate && filters.EndDate != "" && filters.EndDate != filters.StartDate {
		filenameDate = filters.StartDate + "_to_" + filters.EndDate
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="comments-`+filenameDate+`.csv"`)
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})

	writer := csv.NewWriter(c.Writer)
	defer writer.Flush()
	_ = writer.Write([]string{"ID", "UID", "SĐT", "Tên bài", "Link bài", "Tên FB", "Link FB", "Comment", "Ngày comment", "Date label", "Thời gian cào", "Permalink"})
	for _, comment := range comments {
		_ = writer.Write([]string{
			strconv.FormatUint(uint64(comment.ID), 10),
			comment.AuthorUID,
			comment.Phone,
			commentPostTitle(comment),
			commentPostURL(comment),
			comment.Author,
			comment.ProfileURL,
			comment.CommentText,
			commentDateText(comment),
			comment.DateLabel,
			comment.ScrapedAt.Format("2006-01-02 15:04:05"),
			comment.Permalink,
		})
	}
}

func GetComment(c *gin.Context) {
	comment, ok := findComment(c)
	if !ok {
		return
	}

	c.JSON(http.StatusOK, gin.H{"comment": comment})
}

func CreateComment(c *gin.Context) {
	request, ok := bindCommentRequest(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dữ liệu comment không hợp lệ"})
		return
	}

	comment, ok := buildCommentFromRequest(c, request, model.Comment{})
	if !ok {
		return
	}

	exists, err := commentKeyExists(comment.CommentKey, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể kiểm tra trùng comment"})
		return
	}
	if exists {
		c.JSON(http.StatusConflict, gin.H{"error": "Comment đã tồn tại theo comment_key"})
		return
	}

	err = model.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&comment).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể tạo comment"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"comment": comment})
}

func UpdateComment(c *gin.Context) {
	comment, ok := findComment(c)
	if !ok {
		return
	}

	request, ok := bindCommentRequest(c)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Dữ liệu comment không hợp lệ"})
		return
	}

	updated, ok := buildCommentFromRequest(c, request, comment)
	if !ok {
		return
	}

	exists, err := commentKeyExists(updated.CommentKey, updated.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể kiểm tra trùng comment"})
		return
	}
	if exists {
		c.JSON(http.StatusConflict, gin.H{"error": "Comment đã tồn tại theo comment_key"})
		return
	}

	if err := model.DB.Save(&updated).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể cập nhật comment"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"comment": updated})
}

func DeleteComment(c *gin.Context) {
	comment, ok := findComment(c)
	if !ok {
		return
	}

	if err := model.DB.Delete(&comment).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể xóa comment"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Đã xóa comment"})
}

func commentPostTitle(comment model.Comment) string {
	if strings.TrimSpace(comment.Link.Title) != "" {
		return strings.TrimSpace(comment.Link.Title)
	}
	return "Link Facebook chưa đặt tiêu đề"
}

func commentPostURL(comment model.Comment) string {
	if strings.TrimSpace(comment.Link.FinalURL) != "" {
		return strings.TrimSpace(comment.Link.FinalURL)
	}
	return strings.TrimSpace(comment.Link.URL)
}

func normalizeCommentTimes(comments []model.Comment) {
	for i := range comments {
		if comments[i].FacebookCreatedAt != nil {
			continue
		}
		if resolved := model.ResolveFacebookCommentTime(comments[i].ScrapedAt, comments[i].DateLabel); resolved != nil {
			comments[i].FacebookCreatedAt = resolved
		}
	}
}

func commentDateText(comment model.Comment) string {
	if comment.FacebookCreatedAt != nil {
		return comment.FacebookCreatedAt.In(time.Local).Format("2006-01-02 15:04:05")
	}
	if resolved := model.ResolveFacebookCommentTime(comment.ScrapedAt, comment.DateLabel); resolved != nil {
		return resolved.In(time.Local).Format("2006-01-02 15:04:05")
	}
	if strings.TrimSpace(comment.DateLabel) != "" {
		return strings.TrimSpace(comment.DateLabel)
	}
	if !comment.ScrapedAt.IsZero() {
		return comment.ScrapedAt.In(time.Local).Format("2006-01-02 15:04:05")
	}
	return ""
}

func commentKeyExists(commentKey string, excludeID uint) (bool, error) {
	var count int64
	query := model.DB.Model(&model.Comment{}).Where("comment_key = ?", commentKey)
	if excludeID != 0 {
		query = query.Where("id <> ?", excludeID)
	}
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func findComment(c *gin.Context) (model.Comment, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ID comment không hợp lệ"})
		return model.Comment{}, false
	}

	var comment model.Comment
	if err := scopedCommentsQuery(currentUser(c)).First(&comment, uint(id)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Không tìm thấy comment"})
			return model.Comment{}, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể lấy thông tin comment"})
		return model.Comment{}, false
	}

	if comment.FacebookCreatedAt == nil {
		if resolved := model.ResolveFacebookCommentTime(comment.ScrapedAt, comment.DateLabel); resolved != nil {
			comment.FacebookCreatedAt = resolved
		}
	}

	return comment, true
}

func bindCommentRequest(c *gin.Context) (CommentRequest, bool) {
	var request CommentRequest
	if wantsJSON(c) {
		return request, c.ShouldBindJSON(&request) == nil
	}

	linkID, _ := strconv.ParseUint(c.PostForm("link_id"), 10, 64)
	request.LinkID = uint(linkID)
	request.CommentKey = c.PostForm("comment_key")
	request.Author = c.PostForm("author")
	request.AuthorUID = c.PostForm("author_uid")
	request.Phone = c.PostForm("phone")
	request.CommentText = c.PostForm("comment_text")
	request.DateLabel = c.PostForm("date_label")
	request.RawText = c.PostForm("raw_text")
	request.ProfileURL = c.PostForm("profile_url")
	request.Permalink = c.PostForm("permalink")
	request.FacebookCreatedAt = c.PostForm("facebook_created_at")
	return request, true
}

func buildCommentFromRequest(c *gin.Context, request CommentRequest, current model.Comment) (model.Comment, bool) {
	comment := current
	if request.LinkID != 0 {
		comment.LinkID = request.LinkID
	}
	if comment.LinkID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "link_id là bắt buộc"})
		return model.Comment{}, false
	}

	ok, err := canAccessLink(currentUser(c), comment.LinkID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Không thể kiểm tra quyền link"})
		return model.Comment{}, false
	}
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Link không tồn tại hoặc không được gán cho user"})
		return model.Comment{}, false
	}

	comment.Author = strings.TrimSpace(request.Author)
	comment.AuthorUID = strings.TrimSpace(request.AuthorUID)
	comment.Phone = strings.TrimSpace(request.Phone)
	comment.CommentText = strings.TrimSpace(request.CommentText)
	comment.DateLabel = strings.TrimSpace(request.DateLabel)
	comment.RawText = strings.TrimSpace(request.RawText)
	comment.ProfileURL = strings.TrimSpace(request.ProfileURL)
	comment.Permalink = NormalizeCommentPermalink(request.Permalink)
	comment.CommentKey = NormalizeCommentKey(request.CommentKey)
	comment.FacebookCreatedAt = model.ParseFacebookTime(request.FacebookCreatedAt)
	if comment.AuthorUID == "" {
		comment.AuthorUID = model.ExtractFacebookUID(comment.ProfileURL)
	}
	if comment.Phone == "" {
		comment.Phone = model.ExtractPhone(comment.CommentText, comment.RawText)
	}

	if comment.CommentText == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "comment_text là bắt buộc"})
		return model.Comment{}, false
	}
	if comment.CommentKey == "" {
		comment.CommentKey = GenerateCommentKey(comment.LinkID, comment.ProfileURL, comment.Author, comment.CommentText, comment.RawText, comment.Permalink)
	}
	if comment.FirstSeenAt.IsZero() {
		comment.FirstSeenAt = time.Now().UTC()
	}
	if comment.ScrapedAt.IsZero() {
		comment.ScrapedAt = time.Now().UTC()
	}
	if comment.FacebookCreatedAt == nil {
		if resolved := model.ResolveFacebookCommentTime(comment.ScrapedAt, comment.DateLabel); resolved != nil {
			comment.FacebookCreatedAt = resolved
		}
	}

	return comment, true
}

func GenerateCommentKey(linkID uint, profileURL, author, commentText, rawText, permalink string) string {
	if commentID := ExtractCommentIDFromPermalink(permalink); commentID != "" {
		return commentID
	}
	if permalink = NormalizeCommentPermalink(permalink); permalink != "" {
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

func ExtractCommentIDFromPermalink(raw string) string {
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

func NormalizeCommentPermalink(raw string) string {
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

func NormalizeCommentKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if commentID := ExtractCommentIDFromPermalink(raw); commentID != "" {
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
