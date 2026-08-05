package model

import (
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Username     string    `gorm:"uniqueIndex;not null;size:100" json:"username"`
	PasswordHash string    `gorm:"not null" json:"-"`
	Role         string    `gorm:"not null;size:30;default:user;index" json:"role"`
	LinkOnLimit  int       `gorm:"not null;default:0" json:"link_on_limit"`
	LinkOffLimit int       `gorm:"not null;default:0" json:"link_off_limit"`
	LikeLimit    int       `gorm:"not null;default:0" json:"like_limit"`
	DailyLimit   int       `gorm:"not null;default:0" json:"daily_limit"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func NormalizeRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		return RoleUser
	}
	return role
}

func IsValidRole(role string) bool {
	role = NormalizeRole(role)
	return role == RoleAdmin || role == RoleUser
}

func (u User) IsAdmin() bool {
	return NormalizeRole(u.Role) == RoleAdmin
}

func (u *User) SetPassword(password string) error {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	u.PasswordHash = string(hashedPassword)
	return nil
}

func (u *User) CheckPassword(password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
}
