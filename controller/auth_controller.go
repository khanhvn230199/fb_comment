package controller

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"fb_comment/model"
	"fb_comment/scraper"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

type LoginRequest struct {
	Username string `json:"username" form:"username" binding:"required"`
	Password string `json:"password" form:"password" binding:"required"`
}

type UpdatePasswordRequest struct {
	OldPassword     string `json:"old_password" form:"old_password" binding:"required"`
	NewPassword     string `json:"new_password" form:"new_password" binding:"required"`
	ConfirmPassword string `json:"confirm_password" form:"confirm_password" binding:"required"`
}

type UserClaims struct {
	UserID   uint   `json:"user_id"`
	Username string `json:"username"`
	jwt.RegisteredClaims
}

func RegisterRoutes(router *gin.Engine, commentScraper scraper.CommentScraper) {
	router.GET("/", ShowLogin)
	router.GET("/login", ShowLogin)
	router.POST("/login", Login)

	authenticated := router.Group("")
	authenticated.Use(AuthMiddleware())
	{
		authenticated.GET("/profile", ShowProfile)
		authenticated.GET("/users", ListUsers)
		authenticated.GET("/password", ShowUpdatePassword)
		authenticated.POST("/password", UpdatePassword)
		authenticated.POST("/logout", Logout)

		authenticated.GET("/links", ListLinks)
		authenticated.GET("/links/:id/edit", ShowEditLink)
		authenticated.POST("/links/:id", UpdateLink)
		authenticated.POST("/links/:id/toggle", ToggleLink)
		authenticated.POST("/links-bulk-toggle", BulkToggleLinks)

		authenticated.GET("/comments", ShowCommentsPage)
		authenticated.GET("/comments/export", ExportComments)

		authenticated.GET("/resources", ListResources)
		authenticated.POST("/resources/import", ImportResources)
		authenticated.POST("/resources-bulk-delete", BulkDeleteResources)
		authenticated.GET("/resources/:id/edit", ShowEditResource)
		authenticated.POST("/resources/:id", UpdateResource)
		authenticated.POST("/resources/:id/delete", DeleteResource)
		authenticated.POST("/resources/delete-by-status", DeleteResourcesByStatus)

		admin := authenticated.Group("")
		admin.Use(AdminMiddleware())
		{
			admin.GET("/settings", ShowSettings)
			admin.POST("/settings", UpdateSettings)

			admin.POST("/links", CreateLink)
			admin.POST("/links/:id/delete", DeleteLink)

			admin.GET("/users/new", ShowNewUser)
			admin.POST("/users", CreateUser)
			admin.POST("/users-bulk-delete", BulkDeleteUsers)
			admin.GET("/users/:id/edit", ShowEditUser)
			admin.POST("/users/:id", UpdateUser)
			admin.POST("/users/:id/delete", DeleteUser)

			admin.POST("/api/links", CreateLink)
			admin.DELETE("/api/links/:id", DeleteLink)

			admin.GET("/api/users", ListUsers)
			admin.POST("/api/users", CreateUser)
			admin.GET("/api/users/:id", GetUser)
			admin.POST("/api/users/bulk-delete", BulkDeleteUsers)
			admin.PATCH("/api/users/:id", UpdateUser)
			admin.DELETE("/api/users/:id", DeleteUser)

			admin.POST("/api/comments", CreateComment)
			admin.PATCH("/api/comments/:id", UpdateComment)
			admin.DELETE("/api/comments/:id", DeleteComment)

		}

		authenticated.GET("/api/links", ListLinks)
		authenticated.PATCH("/api/links/:id", UpdateLink)
		authenticated.PATCH("/api/links-bulk-toggle", BulkToggleLinks)

		authenticated.GET("/api/comments", ListComments)
		authenticated.GET("/api/comments/:id", GetComment)

		authenticated.GET("/api/resources", ListResources)
		authenticated.POST("/api/resources/import", ImportResources)
		authenticated.POST("/api/resources/bulk-delete", BulkDeleteResources)
		authenticated.GET("/api/resources/:id", GetResource)
		authenticated.PATCH("/api/resources/:id", UpdateResource)
		authenticated.DELETE("/api/resources/:id", DeleteResource)
		authenticated.DELETE("/api/resources", DeleteResourcesByStatus)

		authenticated.GET("/api/facebook/token", GetFacebookToken)
		authenticated.PUT("/api/facebook/token", UpsertFacebookToken)
		authenticated.DELETE("/api/facebook/token", DeleteFacebookToken)
		authenticated.GET("/api/facebook/posts/:post_id/comments", FetchFacebookPostComments)

		RegisterScrapeRoutes(authenticated, commentScraper)
	}
}

func ShowLogin(c *gin.Context) {
	c.HTML(http.StatusOK, "login.html", gin.H{
		"error": c.Query("error"),
	})
}

func Login(c *gin.Context) {
	request, ok := bindLoginRequest(c)
	if !ok {
		respondAuthError(c, http.StatusBadRequest, "Vui lòng nhập tài khoản và mật khẩu")
		return
	}

	var user model.User
	err := model.DB.Where("username = ?", request.Username).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		respondAuthError(c, http.StatusUnauthorized, "Tài khoản hoặc mật khẩu không đúng")
		return
	}
	if err != nil {
		respondAuthError(c, http.StatusInternalServerError, "Không thể đăng nhập")
		return
	}
	if !user.CheckPassword(request.Password) {
		respondAuthError(c, http.StatusUnauthorized, "Tài khoản hoặc mật khẩu không đúng")
		return
	}

	token, err := createJWT(user)
	if err != nil {
		respondAuthError(c, http.StatusInternalServerError, "Không thể tạo token")
		return
	}

	c.SetCookie("token", token, int((24 * time.Hour).Seconds()), "/", "", false, true)
	if wantsJSON(c) {
		c.JSON(http.StatusOK, gin.H{"token": token, "user": userResponse(user)})
		return
	}

	c.Redirect(http.StatusFound, "/comments")
}

func ShowProfile(c *gin.Context) {
	user := currentUser(c)
	c.HTML(http.StatusOK, "profile.html", gin.H{
		"user":    user,
		"success": c.Query("success"),
		"error":   c.Query("error"),
	})
}

func ShowUpdatePassword(c *gin.Context) {
	c.HTML(http.StatusOK, "password.html", gin.H{
		"currentUser": currentUser(c),
		"error":       c.Query("error"),
		"success":     c.Query("success"),
	})
}

func UpdatePassword(c *gin.Context) {
	request, ok := bindUpdatePasswordRequest(c)
	if !ok {
		respondPasswordError(c, http.StatusBadRequest, "Vui lòng nhập đầy đủ thông tin")
		return
	}
	if len(request.NewPassword) < 6 {
		respondPasswordError(c, http.StatusBadRequest, "Mật khẩu mới phải có ít nhất 6 ký tự")
		return
	}
	if request.NewPassword != request.ConfirmPassword {
		respondPasswordError(c, http.StatusBadRequest, "Xác nhận mật khẩu không khớp")
		return
	}

	user := currentUser(c)
	if !user.CheckPassword(request.OldPassword) {
		respondPasswordError(c, http.StatusUnauthorized, "Mật khẩu hiện tại không đúng")
		return
	}

	if err := user.SetPassword(request.NewPassword); err != nil {
		respondPasswordError(c, http.StatusInternalServerError, "Không thể mã hóa mật khẩu mới")
		return
	}
	if err := model.DB.Save(&user).Error; err != nil {
		respondPasswordError(c, http.StatusInternalServerError, "Không thể cập nhật mật khẩu")
		return
	}

	if wantsJSON(c) {
		c.JSON(http.StatusOK, gin.H{"message": "Cập nhật mật khẩu thành công"})
		return
	}
	c.Redirect(http.StatusFound, "/users?success=password_updated")
}

func Logout(c *gin.Context) {
	c.SetCookie("token", "", -1, "/", "", false, true)
	if wantsJSON(c) {
		c.JSON(http.StatusOK, gin.H{"message": "Đã đăng xuất"})
		return
	}
	c.Redirect(http.StatusFound, "/login")
}

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, err := parseToken(tokenFromRequest(c))
		if err != nil {
			requireLogin(c)
			return
		}

		var user model.User
		if err := model.DB.First(&user, claims.UserID).Error; err != nil {
			requireLogin(c)
			return
		}

		c.Set("user", user)
		c.Next()
	}
}

func AdminMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := currentUser(c)
		if !user.IsAdmin() {
			if wantsJSON(c) || strings.HasPrefix(c.FullPath(), "/api/") {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Bạn không có quyền quản trị"})
				return
			}

			c.String(http.StatusForbidden, "Bạn không có quyền quản trị")
			c.Abort()
			return
		}

		c.Next()
	}
}

func currentUser(c *gin.Context) model.User {
	value, exists := c.Get("user")
	if !exists {
		return model.User{}
	}
	user, _ := value.(model.User)
	return user
}

func bindLoginRequest(c *gin.Context) (LoginRequest, bool) {
	var request LoginRequest
	if wantsJSON(c) {
		return request, c.ShouldBindJSON(&request) == nil
	}

	request.Username = strings.TrimSpace(c.PostForm("username"))
	request.Password = c.PostForm("password")
	return request, request.Username != "" && request.Password != ""
}

func bindUpdatePasswordRequest(c *gin.Context) (UpdatePasswordRequest, bool) {
	var request UpdatePasswordRequest
	if wantsJSON(c) {
		return request, c.ShouldBindJSON(&request) == nil
	}

	request.OldPassword = c.PostForm("old_password")
	request.NewPassword = c.PostForm("new_password")
	request.ConfirmPassword = c.PostForm("confirm_password")
	return request, request.OldPassword != "" && request.NewPassword != "" && request.ConfirmPassword != ""
}

func createJWT(user model.User) (string, error) {
	claims := UserClaims{
		UserID:   user.ID,
		Username: user.Username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   user.Username,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret())
}

func parseToken(tokenValue string) (*UserClaims, error) {
	if tokenValue == "" {
		return nil, errors.New("missing token")
	}

	claims := &UserClaims{}
	token, err := jwt.ParseWithClaims(tokenValue, claims, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return jwtSecret(), nil
	})
	if err != nil || !token.Valid {
		return nil, errors.New("invalid token")
	}

	return claims, nil
}

func tokenFromRequest(c *gin.Context) string {
	authorization := c.GetHeader("Authorization")
	if tokenValue, ok := strings.CutPrefix(authorization, "Bearer "); ok {
		return tokenValue
	}

	token, _ := c.Cookie("token")
	return token
}

func requireLogin(c *gin.Context) {
	if wantsJSON(c) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Vui lòng đăng nhập"})
		return
	}

	c.Redirect(http.StatusFound, "/login?error=login_required")
	c.Abort()
}

func respondAuthError(c *gin.Context, status int, message string) {
	if wantsJSON(c) {
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.HTML(status, "login.html", gin.H{"error": message})
}

func respondPasswordError(c *gin.Context, status int, message string) {
	if wantsJSON(c) {
		c.JSON(status, gin.H{"error": message})
		return
	}

	c.HTML(status, "password.html", gin.H{"currentUser": currentUser(c), "error": message})
}

func wantsJSON(c *gin.Context) bool {
	contentType := c.GetHeader("Content-Type")
	accept := c.GetHeader("Accept")
	return strings.Contains(contentType, "application/json") || strings.Contains(accept, "application/json")
}

func jwtSecret() []byte {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		secret = "change-this-secret-in-production"
	}
	return []byte(secret)
}
