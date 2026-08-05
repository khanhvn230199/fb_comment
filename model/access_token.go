package model

import (
	"strings"
	"time"
)

const (
	AccessTokenProviderFacebook = "facebook"
	DefaultAccessTokenName      = "default"

	AccessTokenStatusActive   = "active"
	AccessTokenStatusInactive = "inactive"
	AccessTokenStatusError    = "error"
)

type AccessToken struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	UserID     uint       `gorm:"not null;index;uniqueIndex:idx_user_provider_name" json:"user_id"`
	User       User       `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	Provider   string     `gorm:"not null;size:30;default:facebook;index;uniqueIndex:idx_user_provider_name" json:"provider"`
	Name       string     `gorm:"not null;size:100;default:default;uniqueIndex:idx_user_provider_name" json:"name"`
	Status     string     `gorm:"not null;size:30;default:active;index" json:"status"`
	Token      string     `gorm:"not null;type:text" json:"-"`
	TokenHash  string     `gorm:"not null;size:64;index" json:"token_hash"`
	LastUsedAt *time.Time `json:"last_used_at"`
	LastError  string     `gorm:"type:text" json:"last_error"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

func NormalizeAccessTokenProvider(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return AccessTokenProviderFacebook
	}
	return value
}

func NormalizeAccessTokenName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultAccessTokenName
	}
	return value
}

func NormalizeAccessTokenStatus(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return AccessTokenStatusActive
	}
	return value
}

func IsValidAccessTokenStatus(value string) bool {
	switch NormalizeAccessTokenStatus(value) {
	case AccessTokenStatusActive, AccessTokenStatusInactive, AccessTokenStatusError:
		return true
	default:
		return false
	}
}

func (a *AccessToken) Normalize() {
	a.Provider = NormalizeAccessTokenProvider(a.Provider)
	a.Name = NormalizeAccessTokenName(a.Name)
	a.Status = NormalizeAccessTokenStatus(a.Status)
	a.Token = strings.TrimSpace(a.Token)
	if a.TokenHash == "" && a.Token != "" {
		a.TokenHash = HashResourceValue(a.Token)
	}
}
