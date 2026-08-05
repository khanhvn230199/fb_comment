package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"fb_comment/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const maxUserLimit = 1000000

type UserRequest struct {
	Username     string `json:"username" form:"username"`
	Password     string `json:"password" form:"password"`
	Role         string `json:"role" form:"role"`
	LinkOnLimit  int    `json:"link_on_limit" form:"link_on_limit"`
	LinkOffLimit int    `json:"link_off_limit" form:"link_off_limit"`
	LikeLimit    int    `json:"like_limit" form:"like_limit"`
	DailyLimit   int    `json:"daily_limit" form:"daily_limit"`
}

type BulkDeleteUsersRequest struct {
	IDs []uint `json:"ids" form:"ids"`
}

func ListUsers(c *gin.Context) {
	current := currentUser(c)
	pagination := paginationFromRequest(c, 50)
	query := model.DB.Model(&model.User{})
	if !current.IsAdmin() {
		query = query.Where("id = ?", current.ID)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		respondUserError(c, http.StatusInternalServerError, "Không thể đếm danh sách user")
		return
	}
	pagination = completePagination(pagination, total)

	var users []model.User
	if err := query.Order("id ASC").Limit(pagination.Limit).Offset(pagination.Offset).Find(&users).Error; err != nil {
		respondUserError(c, http.StatusInternalServerError, "Không thể lấy danh sách user")
		return
	}

	if isAPIRequest(c) {
		responses := make([]gin.H, 0, len(users))
		for _, user := range users {
			responses = append(responses, userResponse(user))
		}
		c.JSON(http.StatusOK, gin.H{"users": responses, "pagination": pagination})
		return
	}

	c.HTML(http.StatusOK, "users.html", gin.H{
		"users":       users,
		"currentUser": current,
		"pagination":  pagination,
		"prevPageURL": paginationPrevURL(c, pagination),
		"nextPageURL": paginationNextURL(c, pagination),
		"success":     c.Query("success"),
		"error":       c.Query("error"),
	})
}

func ShowNewUser(c *gin.Context) {
	c.HTML(http.StatusOK, "user_new.html", gin.H{
		"currentUser": currentUser(c),
		"error":       c.Query("error"),
	})
}

func GetUser(c *gin.Context) {
	user, ok := findUser(c)
	if !ok {
		return
	}

	c.JSON(http.StatusOK, gin.H{"user": userResponse(user)})
}

func CreateUser(c *gin.Context) {
	request, ok := bindUserRequest(c)
	if !ok {
		respondUserError(c, http.StatusBadRequest, "Dữ liệu user không hợp lệ")
		return
	}

	request.Username = strings.TrimSpace(request.Username)
	request.Role = model.NormalizeRole(request.Role)
	if err := validateUserRequest(request, true); err != nil {
		respondUserError(c, http.StatusBadRequest, err.Error())
		return
	}

	duplicate, err := usernameExists(request.Username, 0)
	if err != nil {
		respondUserError(c, http.StatusInternalServerError, "Không thể kiểm tra username")
		return
	}
	if duplicate {
		respondUserError(c, http.StatusConflict, "Username đã tồn tại")
		return
	}

	user := model.User{
		Username:     request.Username,
		Role:         request.Role,
		LinkOnLimit:  request.LinkOnLimit,
		LinkOffLimit: request.LinkOffLimit,
		LikeLimit:    request.LikeLimit,
		DailyLimit:   request.DailyLimit,
	}
	if err := user.SetPassword(request.Password); err != nil {
		respondUserError(c, http.StatusInternalServerError, "Không thể mã hóa mật khẩu")
		return
	}
	if err := model.DB.Create(&user).Error; err != nil {
		respondUserError(c, http.StatusInternalServerError, "Không thể tạo user")
		return
	}

	if isAPIRequest(c) {
		c.JSON(http.StatusCreated, gin.H{"user": userResponse(user)})
		return
	}
	c.Redirect(http.StatusFound, "/users?success=created")
}

func ShowEditUser(c *gin.Context) {
	user, ok := findUser(c)
	if !ok {
		return
	}

	c.HTML(http.StatusOK, "user_edit.html", gin.H{
		"user":        user,
		"currentUser": currentUser(c),
		"error":       c.Query("error"),
	})
}

func UpdateUser(c *gin.Context) {
	user, ok := findUser(c)
	if !ok {
		return
	}

	request, ok := bindUserRequest(c)
	if !ok {
		respondUserError(c, http.StatusBadRequest, "Dữ liệu user không hợp lệ")
		return
	}

	request.Username = strings.TrimSpace(request.Username)
	request.Role = model.NormalizeRole(request.Role)
	if err := validateUserRequest(request, false); err != nil {
		respondUserError(c, http.StatusBadRequest, err.Error())
		return
	}

	duplicate, err := usernameExists(request.Username, user.ID)
	if err != nil {
		respondUserError(c, http.StatusInternalServerError, "Không thể kiểm tra username")
		return
	}
	if duplicate {
		respondUserError(c, http.StatusConflict, "Username đã tồn tại")
		return
	}

	if user.IsAdmin() && request.Role == model.RoleUser {
		if ok, err := hasOtherAdmin(user.ID); err != nil {
			respondUserError(c, http.StatusInternalServerError, "Không thể kiểm tra quyền admin")
			return
		} else if !ok {
			respondUserError(c, http.StatusConflict, "Không thể hạ quyền admin cuối cùng")
			return
		}
	}

	user.Username = request.Username
	user.Role = request.Role
	user.LinkOnLimit = request.LinkOnLimit
	user.LinkOffLimit = request.LinkOffLimit
	user.LikeLimit = request.LikeLimit
	user.DailyLimit = request.DailyLimit
	if strings.TrimSpace(request.Password) != "" {
		if err := user.SetPassword(request.Password); err != nil {
			respondUserError(c, http.StatusInternalServerError, "Không thể mã hóa mật khẩu")
			return
		}
	}

	if err := model.DB.Save(&user).Error; err != nil {
		respondUserError(c, http.StatusInternalServerError, "Không thể cập nhật user")
		return
	}

	if isAPIRequest(c) {
		c.JSON(http.StatusOK, gin.H{"user": userResponse(user)})
		return
	}
	c.Redirect(http.StatusFound, "/users?success=updated")
}

func BulkDeleteUsers(c *gin.Context) {
	request, ok := bindBulkDeleteUsersRequest(c)
	if !ok {
		respondUserError(c, http.StatusBadRequest, "Dữ liệu xóa user không hợp lệ")
		return
	}
	ids := uniqueUintValues(request.IDs)
	if len(ids) == 0 {
		respondUserError(c, http.StatusBadRequest, "Vui lòng chọn ít nhất 1 user")
		return
	}

	current := currentUser(c)
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var users []model.User
		if err := tx.Where("id IN ?", ids).Find(&users).Error; err != nil {
			return err
		}
		if len(users) != len(ids) {
			return gorm.ErrRecordNotFound
		}
		for _, user := range users {
			if user.ID == current.ID {
				return errDeleteSelf
			}
		}

		var remainingAdmins int64
		if err := tx.Model(&model.User{}).Where("role = ? AND id NOT IN ?", model.RoleAdmin, ids).Count(&remainingAdmins).Error; err != nil {
			return err
		}
		if remainingAdmins == 0 {
			return errDeleteLastAdmin
		}

		return tx.Where("id IN ?", ids).Delete(&model.User{}).Error
	})
	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			respondUserError(c, http.StatusNotFound, "Có user không tồn tại")
		case errors.Is(err, errDeleteSelf):
			respondUserError(c, http.StatusConflict, "Không thể xóa chính tài khoản đang đăng nhập")
		case errors.Is(err, errDeleteLastAdmin):
			respondUserError(c, http.StatusConflict, "Không thể xóa admin cuối cùng")
		default:
			respondUserError(c, http.StatusInternalServerError, "Không thể xóa các user đã chọn")
		}
		return
	}

	if isAPIRequest(c) {
		c.JSON(http.StatusOK, gin.H{"message": "Đã xóa user", "deleted_count": len(ids)})
		return
	}
	c.Redirect(http.StatusFound, "/users?success=deleted_count_"+strconv.Itoa(len(ids)))
}

func DeleteUser(c *gin.Context) {
	id, err := parseUserID(c)
	if err != nil {
		respondUserError(c, http.StatusBadRequest, "ID user không hợp lệ")
		return
	}

	current := currentUser(c)
	var deleted model.User
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.First(&user, id).Error; err != nil {
			return err
		}
		if user.ID == current.ID {
			return errDeleteSelf
		}
		if user.IsAdmin() {
			var count int64
			if err := tx.Model(&model.User{}).Where("role = ? AND id <> ?", model.RoleAdmin, user.ID).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return errDeleteLastAdmin
			}
		}
		if err := tx.Delete(&user).Error; err != nil {
			return err
		}
		deleted = user
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			respondUserError(c, http.StatusNotFound, "Không tìm thấy user")
		case errors.Is(err, errDeleteSelf):
			respondUserError(c, http.StatusConflict, "Không thể xóa chính tài khoản đang đăng nhập")
		case errors.Is(err, errDeleteLastAdmin):
			respondUserError(c, http.StatusConflict, "Không thể xóa admin cuối cùng")
		default:
			respondUserError(c, http.StatusInternalServerError, "Không thể xóa user")
		}
		return
	}

	if isAPIRequest(c) {
		c.JSON(http.StatusOK, gin.H{"message": "Đã xóa user", "user": userResponse(deleted)})
		return
	}
	c.Redirect(http.StatusFound, "/users?success=deleted")
}

var (
	errDeleteSelf      = errors.New("cannot delete current user")
	errDeleteLastAdmin = errors.New("cannot delete last admin")
)

func findUser(c *gin.Context) (model.User, bool) {
	id, err := parseUserID(c)
	if err != nil {
		respondUserError(c, http.StatusBadRequest, "ID user không hợp lệ")
		return model.User{}, false
	}

	var user model.User
	if err := model.DB.First(&user, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondUserError(c, http.StatusNotFound, "Không tìm thấy user")
			return model.User{}, false
		}
		respondUserError(c, http.StatusInternalServerError, "Không thể lấy thông tin user")
		return model.User{}, false
	}

	return user, true
}

func parseUserID(c *gin.Context) (uint, error) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("invalid user id")
	}
	return uint(id), nil
}

func bindBulkDeleteUsersRequest(c *gin.Context) (BulkDeleteUsersRequest, bool) {
	var request BulkDeleteUsersRequest
	if wantsJSON(c) || strings.HasPrefix(c.FullPath(), "/api/") {
		return request, c.ShouldBindJSON(&request) == nil
	}

	request.IDs = parseUintList(c.PostFormArray("ids"))
	return request, true
}

func bindUserRequest(c *gin.Context) (UserRequest, bool) {
	var request UserRequest
	if wantsJSON(c) {
		return request, c.ShouldBindJSON(&request) == nil
	}

	request.Username = c.PostForm("username")
	request.Password = c.PostForm("password")
	request.Role = c.PostForm("role")
	request.LinkOnLimit = atoiWithDefault(c.PostForm("link_on_limit"), 0)
	request.LinkOffLimit = atoiWithDefault(c.PostForm("link_off_limit"), 0)
	request.LikeLimit = atoiWithDefault(c.PostForm("like_limit"), 0)
	request.DailyLimit = atoiWithDefault(c.PostForm("daily_limit"), 0)
	return request, true
}

func validateUserRequest(request UserRequest, creating bool) error {
	if request.Username == "" {
		return errors.New("Vui lòng nhập username")
	}
	if len([]rune(request.Username)) < 3 || len([]rune(request.Username)) > 100 {
		return errors.New("Username phải từ 3 đến 100 ký tự")
	}
	if !model.IsValidRole(request.Role) {
		return errors.New("Role chỉ được là admin hoặc user")
	}
	if creating && strings.TrimSpace(request.Password) == "" {
		return errors.New("Vui lòng nhập mật khẩu")
	}
	if strings.TrimSpace(request.Password) != "" && len([]rune(request.Password)) < 6 {
		return errors.New("Mật khẩu phải có ít nhất 6 ký tự")
	}
	if err := validateLimit("Link on limit", request.LinkOnLimit); err != nil {
		return err
	}
	if err := validateLimit("Link off limit", request.LinkOffLimit); err != nil {
		return err
	}
	if err := validateLimit("Like limit", request.LikeLimit); err != nil {
		return err
	}
	if err := validateLimit("Daily limit", request.DailyLimit); err != nil {
		return err
	}
	return nil
}

func validateLimit(label string, value int) error {
	if value < 0 {
		return fmt.Errorf("%s phải >= 0", label)
	}
	if value > maxUserLimit {
		return fmt.Errorf("%s không được vượt quá %d", label, maxUserLimit)
	}
	return nil
}

func usernameExists(username string, excludeID uint) (bool, error) {
	var count int64
	query := model.DB.Model(&model.User{}).Where("username = ?", username)
	if excludeID != 0 {
		query = query.Where("id <> ?", excludeID)
	}
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func hasOtherAdmin(userID uint) (bool, error) {
	var count int64
	if err := model.DB.Model(&model.User{}).Where("role = ? AND id <> ?", model.RoleAdmin, userID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func userResponse(user model.User) gin.H {
	return gin.H{
		"id":             user.ID,
		"username":       user.Username,
		"role":           model.NormalizeRole(user.Role),
		"link_on_limit":  user.LinkOnLimit,
		"link_off_limit": user.LinkOffLimit,
		"like_limit":     user.LikeLimit,
		"daily_limit":    user.DailyLimit,
		"created_at":     user.CreatedAt,
		"updated_at":     user.UpdatedAt,
	}
}

func isAPIRequest(c *gin.Context) bool {
	return wantsJSON(c) || strings.HasPrefix(c.FullPath(), "/api/") || strings.HasPrefix(c.Request.URL.Path, "/api/")
}

func respondUserError(c *gin.Context, status int, message string) {
	if isAPIRequest(c) {
		c.JSON(status, gin.H{"error": message})
		return
	}

	path := "/users"
	if c.FullPath() == "/users" && c.Request.Method == http.MethodPost {
		path = "/users/new"
	}
	if strings.Contains(c.FullPath(), "/:id") && !strings.Contains(c.FullPath(), "/delete") {
		path = c.Request.URL.Path
	}
	c.Redirect(http.StatusFound, path+"?error="+url.QueryEscape(message))
}
