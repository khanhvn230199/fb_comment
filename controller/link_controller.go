package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fb_comment/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type LinkRequest struct {
	Title               string `json:"title" form:"title"`
	URL                 string `json:"url" form:"url" binding:"required"`
	FinalURL            string `json:"final_url" form:"final_url"`
	UserID              *uint  `json:"user_id" form:"user_id"`
	Active              *bool  `json:"active" form:"active"`
	Status              string `json:"status" form:"status"`
	LastError           string `json:"last_error" form:"last_error"`
	PollIntervalSeconds int    `json:"poll_interval_seconds" form:"poll_interval_seconds"`
	MaxComments         int    `json:"max_comments" form:"max_comments"`
	MaxScrolls          int    `json:"max_scrolls" form:"max_scrolls"`
	IdlePasses          int    `json:"idle_passes" form:"idle_passes"`
}

type BulkLinkToggleRequest struct {
	IDs    []uint `json:"ids" form:"ids" binding:"required"`
	Active *bool  `json:"active" form:"active" binding:"required"`
}

func ListLinks(c *gin.Context) {
	user := currentUser(c)
	isAPI := wantsJSON(c) || strings.HasPrefix(c.FullPath(), "/api/")
	activeFilter, hasActiveFilter := parseActiveFilter(c)
	pagination := paginationFromRequest(c, 50)

	query := scopedLinksQuery(user)
	if hasActiveFilter || !isAPI {
		query = query.Where("links.active = ?", activeFilter)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondLinkError(c, http.StatusInternalServerError, "Không thể đếm danh sách link")
		return
	}
	pagination = completePagination(pagination, total)

	var links []model.Link
	if err := query.Preload("User").Order("links.created_at DESC").Limit(pagination.Limit).Offset(pagination.Offset).Find(&links).Error; err != nil {
		respondLinkError(c, http.StatusInternalServerError, "Không thể lấy danh sách link")
		return
	}

	if isAPI {
		c.JSON(http.StatusOK, gin.H{"links": links, "pagination": pagination})
		return
	}

	activeCount, inactiveCount := linkStatusCounts(user)
	c.HTML(http.StatusOK, "links.html", gin.H{
		"links":         links,
		"users":         assignableUsers(),
		"currentUser":   user,
		"activeFilter":  strconv.FormatBool(activeFilter),
		"activeCount":   activeCount,
		"inactiveCount": inactiveCount,
		"pagination":    pagination,
		"prevPageURL":   paginationPrevURL(c, pagination),
		"nextPageURL":   paginationNextURL(c, pagination),
		"success":       c.Query("success"),
		"error":         c.Query("error"),
	})
}

func CreateLink(c *gin.Context) {
	if !currentUser(c).IsAdmin() {
		respondLinkError(c, http.StatusForbidden, "Chỉ admin mới được thêm link")
		return
	}

	request, ok := bindLinkRequest(c)
	if !ok || strings.TrimSpace(request.URL) == "" {
		respondLinkError(c, http.StatusBadRequest, "Vui lòng nhập link")
		return
	}

	request.Title = strings.TrimSpace(request.Title)
	request.URL = strings.TrimSpace(request.URL)
	if !isValidFacebookURL(request.URL) {
		respondLinkError(c, http.StatusBadRequest, "Link phải là URL Facebook hợp lệ")
		return
	}
	if err := validateLinkOwner(request.UserID); err != nil {
		respondLinkError(c, http.StatusBadRequest, err.Error())
		return
	}

	var existing model.Link
	err := model.DB.Where("url = ?", request.URL).First(&existing).Error
	if err == nil {
		setLinkActiveState(&existing, true)
		applyLinkRequest(&existing, request, true)
		existing.Normalize()
		if err := model.DB.Save(&existing).Error; err != nil {
			respondLinkError(c, http.StatusInternalServerError, "Không thể kích hoạt lại link")
			return
		}
		respondLinkSuccess(c, http.StatusOK, existing, "reactivated")
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		respondLinkError(c, http.StatusInternalServerError, "Không thể kiểm tra link")
		return
	}

	link := model.NewLink(request.URL)
	applyLinkRequest(&link, request, true)
	link.Normalize()
	if err := model.DB.Create(&link).Error; err != nil {
		respondLinkError(c, http.StatusInternalServerError, "Không thể tạo link")
		return
	}

	respondLinkSuccess(c, http.StatusCreated, link, "created")
}

func ShowEditLink(c *gin.Context) {
	link, ok := findLink(c)
	if !ok {
		return
	}

	c.HTML(http.StatusOK, "link_edit.html", gin.H{
		"link":           link,
		"users":          assignableUsers(),
		"currentUser":    currentUser(c),
		"assignedUserID": optionalUintString(link.UserID),
		"activeFilter":   strconv.FormatBool(activeFilterFromLinkPage(c)),
		"backPath":       linkListPath(c),
		"error":          c.Query("error"),
	})
}

func UpdateLink(c *gin.Context) {
	current := currentUser(c)
	link, ok := findLink(c)
	if !ok {
		return
	}

	request, ok := bindLinkRequest(c)
	if !ok {
		respondLinkError(c, http.StatusBadRequest, "Dữ liệu link không hợp lệ")
		return
	}

	request.Title = strings.TrimSpace(request.Title)
	request.URL = strings.TrimSpace(request.URL)
	if request.URL == "" {
		respondLinkError(c, http.StatusBadRequest, "Vui lòng nhập link")
		return
	}
	if !isValidFacebookURL(request.URL) {
		respondLinkError(c, http.StatusBadRequest, "Link phải là URL Facebook hợp lệ")
		return
	}
	if current.IsAdmin() {
		if err := validateLinkOwner(request.UserID); err != nil {
			respondLinkError(c, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		request.URL = link.URL
		request.UserID = link.UserID
	}

	applyLinkRequest(&link, request, current.IsAdmin())
	link.Normalize()
	if err := model.DB.Save(&link).Error; err != nil {
		respondLinkError(c, http.StatusInternalServerError, "Không thể cập nhật link")
		return
	}

	respondLinkSuccess(c, http.StatusOK, link, "updated")
}

func DeleteLink(c *gin.Context) {
	if !canDeleteLink(currentUser(c)) {
		respondLinkError(c, http.StatusForbidden, "Chỉ admin mới được xóa link")
		return
	}

	link, ok := findLink(c)
	if !ok {
		return
	}

	if err := model.DB.Delete(&link).Error; err != nil {
		respondLinkError(c, http.StatusInternalServerError, "Không thể xóa link")
		return
	}

	if wantsJSON(c) || strings.HasPrefix(c.FullPath(), "/api/") {
		c.JSON(http.StatusOK, gin.H{"message": "Đã xóa link"})
		return
	}
	c.Redirect(http.StatusFound, linkListPath(c)+"&success=deleted")
}

func ToggleLink(c *gin.Context) {
	link, ok := findLink(c)
	if !ok {
		return
	}

	setLinkActiveState(&link, !link.Active)
	link.Normalize()
	if err := model.DB.Save(&link).Error; err != nil {
		respondLinkError(c, http.StatusInternalServerError, "Không thể đổi trạng thái link")
		return
	}

	respondLinkSuccess(c, http.StatusOK, link, "toggled")
}

func BulkToggleLinks(c *gin.Context) {
	request, ok := bindBulkLinkToggleRequest(c)
	if !ok || request.Active == nil {
		respondLinkError(c, http.StatusBadRequest, "Dữ liệu đổi trạng thái không hợp lệ")
		return
	}

	ids := uniqueUintValues(request.IDs)
	if len(ids) == 0 {
		respondLinkError(c, http.StatusBadRequest, "Vui lòng chọn ít nhất 1 link")
		return
	}

	links, err := findScopedLinksByIDs(currentUser(c), ids)
	if err != nil {
		respondLinkError(c, http.StatusForbidden, err.Error())
		return
	}

	for index := range links {
		setLinkActiveState(&links[index], *request.Active)
		links[index].Normalize()
		if err := model.DB.Save(&links[index]).Error; err != nil {
			respondLinkError(c, http.StatusInternalServerError, "Không thể cập nhật trạng thái link")
			return
		}
	}

	if wantsJSON(c) || strings.HasPrefix(c.FullPath(), "/api/") {
		c.JSON(http.StatusOK, gin.H{
			"message": updatedBulkLinkMessage(*request.Active, len(links)),
			"links":   links,
		})
		return
	}

	success := "bulk_disabled"
	if *request.Active {
		success = "bulk_enabled"
	}
	c.Redirect(http.StatusFound, linkListPath(c)+"&success="+url.QueryEscape(success))
}

func parseActiveFilter(c *gin.Context) (bool, bool) {
	value := strings.TrimSpace(strings.ToLower(c.Query("active")))
	if value == "" {
		return true, false
	}

	return value == "true" || value == "1" || value == "active" || value == "on", true
}

func linkStatusCounts(user model.User) (int64, int64) {
	var activeCount int64
	var inactiveCount int64
	_ = scopedLinksQuery(user).Where("links.active = ?", true).Count(&activeCount).Error
	_ = scopedLinksQuery(user).Where("links.active = ?", false).Count(&inactiveCount).Error
	return activeCount, inactiveCount
}

func linkListPath(c *gin.Context) string {
	activeFilter, hasActiveFilter := parseActiveFilter(c)
	if !hasActiveFilter {
		activeFilter = true
	}
	return "/links?active=" + strconv.FormatBool(activeFilter)
}

func activeFilterFromLinkPage(c *gin.Context) bool {
	activeFilter, hasActiveFilter := parseActiveFilter(c)
	if !hasActiveFilter {
		return true
	}
	return activeFilter
}

func findLink(c *gin.Context) (model.Link, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		respondLinkError(c, http.StatusBadRequest, "ID link không hợp lệ")
		return model.Link{}, false
	}

	var link model.Link
	query := scopedLinksQuery(currentUser(c)).Preload("User")
	if err := query.First(&link, uint(id)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondLinkError(c, http.StatusNotFound, "Không tìm thấy link")
			return model.Link{}, false
		}
		respondLinkError(c, http.StatusInternalServerError, "Không thể lấy thông tin link")
		return model.Link{}, false
	}

	return link, true
}

func bindLinkRequest(c *gin.Context) (LinkRequest, bool) {
	var request LinkRequest
	if wantsJSON(c) {
		return request, c.ShouldBindJSON(&request) == nil
	}

	request.Title = c.PostForm("title")
	request.URL = c.PostForm("url")
	request.FinalURL = c.PostForm("final_url")
	request.UserID = parseOptionalUint(c.PostForm("user_id"))
	request.Status = c.PostForm("status")
	request.LastError = c.PostForm("last_error")
	request.PollIntervalSeconds = atoiWithDefault(c.PostForm("poll_interval_seconds"), model.DefaultLinkPollIntervalSeconds)
	request.MaxComments = atoiWithDefault(c.PostForm("max_comments"), 50)
	request.MaxScrolls = atoiWithDefault(c.PostForm("max_scrolls"), 20)
	request.IdlePasses = atoiWithDefault(c.PostForm("idle_passes"), 2)
	if c.PostForm("active_present") != "" {
		active := c.PostForm("active") == "on" || c.PostForm("active") == "true" || c.PostForm("active") == "1"
		request.Active = &active
	}
	return request, true
}

func bindBulkLinkToggleRequest(c *gin.Context) (BulkLinkToggleRequest, bool) {
	var request BulkLinkToggleRequest
	if wantsJSON(c) || strings.HasPrefix(c.FullPath(), "/api/") {
		return request, c.ShouldBindJSON(&request) == nil
	}

	request.IDs = parseUintList(c.PostFormArray("ids"))
	if activeValue := strings.TrimSpace(c.PostForm("active")); activeValue != "" {
		active := activeValue == "on" || activeValue == "true" || activeValue == "1"
		request.Active = &active
	}
	return request, true
}

func applyLinkRequest(link *model.Link, request LinkRequest, allowOwnerChange bool) {
	link.Title = strings.TrimSpace(request.Title)
	link.URL = strings.TrimSpace(request.URL)
	link.FinalURL = strings.TrimSpace(request.FinalURL)
	link.Status = strings.TrimSpace(request.Status)
	link.LastError = strings.TrimSpace(request.LastError)
	link.PollIntervalSeconds = request.PollIntervalSeconds
	link.MaxComments = request.MaxComments
	link.MaxScrolls = request.MaxScrolls
	link.IdlePasses = request.IdlePasses
	if request.Active != nil {
		link.Active = *request.Active
	}
	if allowOwnerChange {
		link.UserID = request.UserID
	}
}

func setLinkActiveState(link *model.Link, active bool) {
	link.Active = active
	if active {
		link.Status = "pending"
		link.LastError = ""
		link.NextScrapeAt = time.Now()
	}
}

func findScopedLinksByIDs(user model.User, ids []uint) ([]model.Link, error) {
	var links []model.Link
	if err := scopedLinksQuery(user).Where("links.id IN ?", ids).Find(&links).Error; err != nil {
		return nil, err
	}
	if len(links) != len(ids) {
		return nil, fmt.Errorf("Có link không tồn tại hoặc bạn không có quyền thao tác")
	}
	return links, nil
}

func updatedBulkLinkMessage(active bool, count int) string {
	if active {
		return fmt.Sprintf("Đã bật %d link", count)
	}
	return fmt.Sprintf("Đã tắt %d link", count)
}

func uniqueUintValues(values []uint) []uint {
	seen := make(map[uint]struct{}, len(values))
	unique := make([]uint, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func parseUintList(values []string) []uint {
	ids := make([]uint, 0, len(values))
	for _, value := range values {
		parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil || parsed == 0 {
			continue
		}
		ids = append(ids, uint(parsed))
	}
	return ids
}

func assignableUsers() []model.User {
	var users []model.User
	_ = model.DB.Order("username ASC").Find(&users).Error
	return users
}

func validateLinkOwner(userID *uint) error {
	if userID == nil {
		return nil
	}
	var count int64
	if err := model.DB.Model(&model.User{}).Where("id = ?", *userID).Count(&count).Error; err != nil {
		return fmt.Errorf("Không thể kiểm tra user được gán")
	}
	if count == 0 {
		return fmt.Errorf("User được gán không tồn tại")
	}
	return nil
}

func parseOptionalUint(value string) *uint {
	value = strings.TrimSpace(value)
	if value == "" || value == "0" {
		return nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 {
		return nil
	}
	result := uint(parsed)
	return &result
}

func optionalUintString(value *uint) string {
	if value == nil {
		return ""
	}
	return strconv.FormatUint(uint64(*value), 10)
}

func atoiWithDefault(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

func isValidFacebookURL(rawURL string) bool {
	rawURL = strings.TrimSpace(strings.ToLower(rawURL))
	return strings.HasPrefix(rawURL, "https://facebook.com/") ||
		strings.HasPrefix(rawURL, "https://www.facebook.com/") ||
		strings.HasPrefix(rawURL, "https://m.facebook.com/") ||
		strings.HasPrefix(rawURL, "https://fb.watch/")
}

func respondLinkSuccess(c *gin.Context, status int, link model.Link, action string) {
	if wantsJSON(c) || strings.HasPrefix(c.FullPath(), "/api/") {
		c.JSON(status, gin.H{"link": link})
		return
	}
	c.Redirect(http.StatusFound, linkListPath(c)+"&success="+url.QueryEscape(action))
}

func respondLinkError(c *gin.Context, status int, message string) {
	if wantsJSON(c) || strings.HasPrefix(c.FullPath(), "/api/") {
		c.JSON(status, gin.H{"error": message})
		return
	}
	c.Redirect(http.StatusFound, linkListPath(c)+"&error="+url.QueryEscape(message))
}
