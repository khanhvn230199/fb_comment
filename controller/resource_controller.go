package controller

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"fb_comment/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const maxResourceValueLength = 8192

type ResourceImportRequest struct {
	UserID *uint    `json:"user_id" form:"user_id"`
	Type   string   `json:"type" form:"type"`
	Status string   `json:"status" form:"status"`
	Value  string   `json:"value" form:"value"`
	Items  []string `json:"items"`
	List   string   `json:"list" form:"list"`
}

type ResourceUpdateRequest struct {
	UserID *uint  `json:"user_id" form:"user_id"`
	Type   string `json:"type" form:"type"`
	Status string `json:"status" form:"status"`
	Value  string `json:"value" form:"value"`
}

type BulkDeleteResourcesRequest struct {
	IDs     []uint `json:"ids" form:"ids"`
	Confirm bool   `json:"confirm" form:"confirm"`
}

type ResourceResponse struct {
	ID            uint       `json:"id"`
	UserID        *uint      `json:"user_id"`
	OwnerUsername string     `json:"owner_username,omitempty"`
	Type          string     `json:"type"`
	Status        string     `json:"status"`
	ValueMasked   string     `json:"value_masked"`
	ValueLength   int        `json:"value_length"`
	LastUsedAt    *time.Time `json:"last_used_at"`
	LastError     string     `json:"last_error"`
	CreatedByID   *uint      `json:"created_by_id"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func ListResources(c *gin.Context) {
	current := currentUser(c)
	resourceType := model.NormalizeResourceType(c.Query("type"))
	status := strings.TrimSpace(c.Query("status"))
	filterUserID := parseOptionalUint(c.Query("user_id"))
	if resourceType != "" && !model.IsValidResourceType(resourceType) {
		respondResourceError(c, http.StatusBadRequest, "Type resource không hợp lệ")
		return
	}
	if status != "" {
		status = model.NormalizeResourceStatus(status)
		if !model.IsValidResourceStatus(status) {
			respondResourceError(c, http.StatusBadRequest, "Status resource không hợp lệ")
			return
		}
	}
	if !current.IsAdmin() && filterUserID != nil && *filterUserID != current.ID {
		respondResourceError(c, http.StatusForbidden, "Bạn không có quyền lọc resource của user khác")
		return
	}

	pagination := paginationFromRequest(c, 100)
	query := scopedResourcesQuery(current)
	if current.IsAdmin() && filterUserID != nil {
		query = query.Where("resources.user_id = ?", *filterUserID)
	}
	if resourceType != "" {
		query = query.Where("resources.type = ?", resourceType)
	}
	if status != "" {
		query = query.Where("resources.status = ?", status)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondResourceError(c, http.StatusInternalServerError, "Không thể đếm danh sách resource")
		return
	}
	pagination = completePagination(pagination, total)

	var resources []model.Resource
	if err := query.Preload("User").Order("resources.created_at DESC, resources.id DESC").Limit(pagination.Limit).Offset(pagination.Offset).Find(&resources).Error; err != nil {
		respondResourceError(c, http.StatusInternalServerError, "Không thể lấy danh sách resource")
		return
	}

	responses := resourceResponses(resources)
	if isAPIRequest(c) {
		c.JSON(http.StatusOK, gin.H{"resources": responses, "pagination": pagination})
		return
	}

	c.HTML(http.StatusOK, "resources.html", gin.H{
		"resources":    responses,
		"users":        assignableUsers(),
		"types":        model.ResourceTypes(),
		"statuses":     model.ResourceStatuses(),
		"filterType":   resourceType,
		"filterStatus": status,
		"filterUserID": optionalUintString(filterUserID),
		"currentUser":  current,
		"isAdmin":      current.IsAdmin(),
		"pagination":   pagination,
		"prevPageURL":  paginationPrevURL(c, pagination),
		"nextPageURL":  paginationNextURL(c, pagination),
		"success":      c.Query("success"),
		"error":        c.Query("error"),
	})
}

func GetResource(c *gin.Context) {
	resource, ok := findResource(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"resource": resourceResponse(resource)})
}

func ImportResources(c *gin.Context) {
	request, ok := bindResourceImportRequest(c)
	if !ok {
		respondResourceError(c, http.StatusBadRequest, "Dữ liệu resource không hợp lệ")
		return
	}

	resourceType := model.NormalizeResourceType(request.Type)
	status := model.NormalizeResourceStatus(request.Status)
	if !model.IsValidResourceType(resourceType) {
		respondResourceError(c, http.StatusBadRequest, "Type resource không hợp lệ")
		return
	}
	if !model.IsValidResourceStatus(status) {
		respondResourceError(c, http.StatusBadRequest, "Status resource không hợp lệ")
		return
	}

	current := currentUser(c)
	ownerID, ok := resourceOwnerIDForRequest(c, current, request.UserID)
	if !ok {
		return
	}

	value, ok := resourceImportValue(c, request, resourceType)
	if !ok {
		return
	}

	valueHash := model.HashResourceValue(value)
	if duplicate, err := resourceDuplicateExists(&ownerID, resourceType, valueHash, 0); err != nil {
		respondResourceError(c, http.StatusInternalServerError, "Không thể kiểm tra trùng resource")
		return
	} else if duplicate {
		respondResourceError(c, http.StatusConflict, "Resource đã tồn tại cho user này")
		return
	}

	createdByID := current.ID
	resource := model.Resource{
		UserID:      &ownerID,
		Type:        resourceType,
		Status:      status,
		Value:       value,
		ValueHash:   valueHash,
		CreatedByID: &createdByID,
	}
	resource.Normalize()
	if err := model.DB.Create(&resource).Error; err != nil {
		respondResourceError(c, http.StatusInternalServerError, "Không thể import resource")
		return
	}
	_ = model.DB.Preload("User").First(&resource, resource.ID).Error

	if isAPIRequest(c) {
		c.JSON(http.StatusCreated, gin.H{"resource": resourceResponse(resource), "user_id": ownerID})
		return
	}

	c.Redirect(http.StatusFound, "/resources?type="+url.QueryEscape(resourceType)+"&status="+url.QueryEscape(status)+"&success=created")
}

func ShowEditResource(c *gin.Context) {
	resource, ok := findResource(c)
	if !ok {
		return
	}
	current := currentUser(c)
	c.HTML(http.StatusOK, "resource_edit.html", gin.H{
		"resource":       resourceResponse(resource),
		"users":          assignableUsers(),
		"types":          model.ResourceTypes(),
		"statuses":       model.ResourceStatuses(),
		"currentUser":    current,
		"isAdmin":        current.IsAdmin(),
		"assignedUserID": optionalUintString(resource.UserID),
		"error":          c.Query("error"),
	})
}

func UpdateResource(c *gin.Context) {
	resource, ok := findResource(c)
	if !ok {
		return
	}

	request, ok := bindResourceUpdateRequest(c)
	if !ok {
		respondResourceError(c, http.StatusBadRequest, "Dữ liệu resource không hợp lệ")
		return
	}

	resourceType := model.NormalizeResourceType(request.Type)
	status := model.NormalizeResourceStatus(request.Status)
	if !model.IsValidResourceType(resourceType) {
		respondResourceError(c, http.StatusBadRequest, "Type resource không hợp lệ")
		return
	}
	if !model.IsValidResourceStatus(status) {
		respondResourceError(c, http.StatusBadRequest, "Status resource không hợp lệ")
		return
	}

	current := currentUser(c)
	ownerID := resource.UserID
	if current.IsAdmin() {
		if request.UserID != nil {
			if err := validateResourceOwner(*request.UserID); err != nil {
				respondResourceError(c, http.StatusBadRequest, err.Error())
				return
			}
			ownerID = request.UserID
		}
	} else {
		if request.UserID != nil && *request.UserID != current.ID {
			respondResourceError(c, http.StatusForbidden, "Bạn không có quyền đổi owner resource")
			return
		}
		ownerID = &current.ID
	}

	value := strings.TrimSpace(request.Value)
	valueHash := resource.ValueHash
	if value != "" {
		if err := validateResourceValue(resourceType, value); err != nil {
			respondResourceError(c, http.StatusBadRequest, err.Error())
			return
		}
		valueHash = model.HashResourceValue(value)
	}
	if duplicate, err := resourceDuplicateExists(ownerID, resourceType, valueHash, resource.ID); err != nil {
		respondResourceError(c, http.StatusInternalServerError, "Không thể kiểm tra trùng resource")
		return
	} else if duplicate {
		respondResourceError(c, http.StatusConflict, "Resource đã tồn tại cho user này")
		return
	}

	resource.UserID = ownerID
	resource.Type = resourceType
	resource.Status = status
	if value != "" {
		resource.Value = value
		resource.ValueHash = valueHash
	}
	resource.Normalize()

	if err := model.DB.Save(&resource).Error; err != nil {
		respondResourceError(c, http.StatusInternalServerError, "Không thể cập nhật resource")
		return
	}

	if isAPIRequest(c) {
		c.JSON(http.StatusOK, gin.H{"resource": resourceResponse(resource)})
		return
	}
	c.Redirect(http.StatusFound, "/resources?success=updated")
}

func BulkDeleteResources(c *gin.Context) {
	request, ok := bindBulkDeleteResourcesRequest(c)
	if !ok {
		respondResourceError(c, http.StatusBadRequest, "Dữ liệu xóa resource không hợp lệ")
		return
	}
	ids := uniqueUintValues(request.IDs)
	if len(ids) == 0 {
		respondResourceError(c, http.StatusBadRequest, "Vui lòng chọn ít nhất 1 resource")
		return
	}

	current := currentUser(c)
	var resources []model.Resource
	if err := scopedResourcesQuery(current).Where("resources.id IN ?", ids).Find(&resources).Error; err != nil {
		respondResourceError(c, http.StatusInternalServerError, "Không thể lấy resource đã chọn")
		return
	}
	if len(resources) != len(ids) {
		respondResourceError(c, http.StatusNotFound, "Có resource không tồn tại hoặc bạn không có quyền xóa")
		return
	}
	for _, resource := range resources {
		if resource.Status == model.ResourceStatusActive && !request.Confirm {
			respondResourceError(c, http.StatusBadRequest, "Xóa resource active cần confirm=true")
			return
		}
	}

	result := scopedResourcesQuery(current).Where("resources.id IN ?", ids).Delete(&model.Resource{})
	if result.Error != nil {
		respondResourceError(c, http.StatusInternalServerError, "Không thể xóa resource đã chọn")
		return
	}

	if isAPIRequest(c) {
		c.JSON(http.StatusOK, gin.H{"deleted_count": result.RowsAffected})
		return
	}
	c.Redirect(http.StatusFound, "/resources?success=deleted_count_"+strconv.FormatInt(result.RowsAffected, 10))
}

func DeleteResource(c *gin.Context) {
	resource, ok := findResource(c)
	if !ok {
		return
	}

	if err := model.DB.Delete(&resource).Error; err != nil {
		respondResourceError(c, http.StatusInternalServerError, "Không thể xóa resource")
		return
	}

	if isAPIRequest(c) {
		c.JSON(http.StatusOK, gin.H{"message": "Đã xóa resource", "resource": resourceResponse(resource)})
		return
	}
	c.Redirect(http.StatusFound, "/resources?success=deleted")
}

func DeleteResourcesByStatus(c *gin.Context) {
	current := currentUser(c)
	status := resourceStatusInput(c)
	resourceType := resourceTypeInput(c)
	filterUserID := parseOptionalUint(resourceUserIDInput(c))
	if status == "" {
		respondResourceError(c, http.StatusBadRequest, "Status là bắt buộc khi xóa theo status")
		return
	}
	status = model.NormalizeResourceStatus(status)
	resourceType = model.NormalizeResourceType(resourceType)
	if !model.IsValidResourceStatus(status) {
		respondResourceError(c, http.StatusBadRequest, "Status resource không hợp lệ")
		return
	}
	if resourceType != "" && !model.IsValidResourceType(resourceType) {
		respondResourceError(c, http.StatusBadRequest, "Type resource không hợp lệ")
		return
	}
	if !current.IsAdmin() && filterUserID != nil && *filterUserID != current.ID {
		respondResourceError(c, http.StatusForbidden, "Bạn không có quyền xóa resource của user khác")
		return
	}
	if status == model.ResourceStatusActive && confirmInput(c) != "true" {
		respondResourceError(c, http.StatusBadRequest, "Xóa resource active cần confirm=true")
		return
	}

	query := scopedResourcesQuery(current).Where("resources.status = ?", status)
	if current.IsAdmin() && filterUserID != nil {
		query = query.Where("resources.user_id = ?", *filterUserID)
	}
	if resourceType != "" {
		query = query.Where("resources.type = ?", resourceType)
	}
	result := query.Delete(&model.Resource{})
	if result.Error != nil {
		respondResourceError(c, http.StatusInternalServerError, "Không thể xóa resource theo status")
		return
	}

	if isAPIRequest(c) {
		c.JSON(http.StatusOK, gin.H{"deleted_count": result.RowsAffected})
		return
	}
	c.Redirect(http.StatusFound, "/resources?success=deleted_count_"+strconv.FormatInt(result.RowsAffected, 10))
}

func findResource(c *gin.Context) (model.Resource, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		respondResourceError(c, http.StatusBadRequest, "ID resource không hợp lệ")
		return model.Resource{}, false
	}

	var resource model.Resource
	query := scopedResourcesQuery(currentUser(c)).Preload("User")
	if err := query.First(&resource, uint(id)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondResourceError(c, http.StatusNotFound, "Không tìm thấy resource")
			return model.Resource{}, false
		}
		respondResourceError(c, http.StatusInternalServerError, "Không thể lấy resource")
		return model.Resource{}, false
	}
	return resource, true
}

func bindResourceImportRequest(c *gin.Context) (ResourceImportRequest, bool) {
	var request ResourceImportRequest
	if wantsJSON(c) {
		return request, c.ShouldBindJSON(&request) == nil
	}

	request.UserID = parseOptionalUint(c.PostForm("user_id"))
	request.Type = c.PostForm("type")
	request.Status = c.PostForm("status")
	request.Value = c.PostForm("value")
	request.List = c.PostForm("list")
	return request, true
}

func bindBulkDeleteResourcesRequest(c *gin.Context) (BulkDeleteResourcesRequest, bool) {
	var request BulkDeleteResourcesRequest
	if wantsJSON(c) || strings.HasPrefix(c.FullPath(), "/api/") {
		return request, c.ShouldBindJSON(&request) == nil
	}

	request.IDs = parseUintList(c.PostFormArray("ids"))
	request.Confirm = c.PostForm("confirm") == "true" || c.PostForm("confirm") == "1" || c.PostForm("confirm") == "on"
	return request, true
}

func bindResourceUpdateRequest(c *gin.Context) (ResourceUpdateRequest, bool) {
	var request ResourceUpdateRequest
	if wantsJSON(c) || strings.HasPrefix(c.FullPath(), "/api/") {
		return request, c.ShouldBindJSON(&request) == nil
	}

	request.UserID = parseOptionalUint(c.PostForm("user_id"))
	request.Type = c.PostForm("type")
	request.Status = c.PostForm("status")
	request.Value = c.PostForm("value")
	return request, true
}

func resourceImportValue(c *gin.Context, request ResourceImportRequest, resourceType string) (string, bool) {
	values := make([]string, 0, 1+len(request.Items))
	if strings.TrimSpace(request.Value) != "" {
		values = append(values, request.Value)
	}
	for _, item := range request.Items {
		if strings.TrimSpace(item) != "" {
			values = append(values, item)
		}
	}
	for line := range strings.SplitSeq(request.List, "\n") {
		if strings.TrimSpace(line) != "" {
			values = append(values, line)
		}
	}

	if len(values) != 1 {
		respondResourceError(c, http.StatusBadRequest, "Mỗi request chỉ được nhập đúng 1 value")
		return "", false
	}
	value := strings.TrimSpace(values[0])
	if err := validateResourceValue(resourceType, value); err != nil {
		respondResourceError(c, http.StatusBadRequest, err.Error())
		return "", false
	}
	return value, true
}

func validateResourceValue(resourceType string, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("Value resource là bắt buộc")
	}
	if !isSingleLineResourceValue(value) {
		return errors.New("Value resource không được xuống dòng")
	}
	if utf8.RuneCountInString(value) > maxResourceValueLength {
		return errors.New("Giá trị resource quá dài")
	}
	if resourceType == model.ResourceTypeToken && containsWhitespace(value) {
		return errors.New("Facebook access token chỉ được nhập 1 token, không được chứa khoảng trắng")
	}
	return nil
}

func isSingleLineResourceValue(value string) bool {
	return !strings.ContainsAny(value, "\r\n")
}

func containsWhitespace(value string) bool {
	return strings.IndexFunc(value, unicode.IsSpace) >= 0
}

func resourceResponse(resource model.Resource) ResourceResponse {
	response := ResourceResponse{
		ID:          resource.ID,
		UserID:      resource.UserID,
		Type:        resource.Type,
		Status:      resource.Status,
		ValueMasked: model.MaskResourceValue(resource.Value),
		ValueLength: utf8.RuneCountInString(strings.TrimSpace(resource.Value)),
		LastUsedAt:  resource.LastUsedAt,
		LastError:   resource.LastError,
		CreatedByID: resource.CreatedByID,
		CreatedAt:   resource.CreatedAt,
		UpdatedAt:   resource.UpdatedAt,
	}
	if resource.User.ID != 0 {
		response.OwnerUsername = resource.User.Username
	}
	return response
}

func resourceResponses(resources []model.Resource) []ResourceResponse {
	responses := make([]ResourceResponse, 0, len(resources))
	for _, resource := range resources {
		responses = append(responses, resourceResponse(resource))
	}
	return responses
}

func resourceOwnerIDForRequest(c *gin.Context, current model.User, requested *uint) (uint, bool) {
	if !current.IsAdmin() {
		if requested != nil && *requested != current.ID {
			respondResourceError(c, http.StatusForbidden, "Bạn không có quyền gán resource cho user khác")
			return 0, false
		}
		return current.ID, true
	}

	if requested == nil {
		return current.ID, true
	}
	if err := validateResourceOwner(*requested); err != nil {
		respondResourceError(c, http.StatusBadRequest, err.Error())
		return 0, false
	}
	return *requested, true
}

func validateResourceOwner(userID uint) error {
	var count int64
	if err := model.DB.Model(&model.User{}).Where("id = ?", userID).Count(&count).Error; err != nil {
		return errors.New("Không thể kiểm tra user được gán")
	}
	if count == 0 {
		return errors.New("User được gán không tồn tại")
	}
	return nil
}

func resourceDuplicateExists(userID *uint, resourceType string, valueHash string, excludeID uint) (bool, error) {
	if userID == nil || valueHash == "" {
		return false, nil
	}
	query := model.DB.Model(&model.Resource{}).
		Where("user_id = ? AND type = ? AND value_hash = ?", *userID, resourceType, valueHash)
	if excludeID != 0 {
		query = query.Where("id <> ?", excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func resourceStatusInput(c *gin.Context) string {
	if value := c.Query("status"); value != "" {
		return value
	}
	return c.PostForm("status")
}

func resourceTypeInput(c *gin.Context) string {
	if value := c.Query("type"); value != "" {
		return value
	}
	return c.PostForm("type")
}

func resourceUserIDInput(c *gin.Context) string {
	if value := c.Query("user_id"); value != "" {
		return value
	}
	return c.PostForm("user_id")
}

func confirmInput(c *gin.Context) string {
	if value := c.Query("confirm"); value != "" {
		return value
	}
	return c.PostForm("confirm")
}

func respondResourceError(c *gin.Context, status int, message string) {
	if isAPIRequest(c) {
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.Redirect(http.StatusFound, "/resources?error="+url.QueryEscape(message))
}
