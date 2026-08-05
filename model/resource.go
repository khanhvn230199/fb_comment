package model

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

const (
	ResourceTypeToken  = "token"
	ResourceTypeProxy  = "proxy"
	ResourceTypeCookie = "cookie"

	ResourceStatusActive   = "active"
	ResourceStatusInactive = "inactive"
	ResourceStatusUsed     = "used"
	ResourceStatusError    = "error"
)

type Resource struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	UserID      *uint      `gorm:"index;uniqueIndex:idx_resource_user_type_hash" json:"user_id"`
	User        User       `gorm:"constraint:OnUpdate:CASCADE,OnDelete:SET NULL;" json:"-"`
	Type        string     `gorm:"not null;size:30;index;uniqueIndex:idx_resource_user_type_hash" json:"type"`
	Status      string     `gorm:"not null;size:30;default:active;index" json:"status"`
	Value       string     `gorm:"not null;type:text" json:"-"`
	ValueHash   string     `gorm:"not null;size:64;uniqueIndex:idx_resource_user_type_hash" json:"value_hash"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	LastError   string     `gorm:"type:text" json:"last_error"`
	CreatedByID *uint      `gorm:"index" json:"created_by_id"`
	CreatedBy   User       `gorm:"constraint:OnUpdate:CASCADE,OnDelete:SET NULL;" json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func NormalizeResourceType(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func IsValidResourceType(value string) bool {
	switch NormalizeResourceType(value) {
	case ResourceTypeToken, ResourceTypeProxy, ResourceTypeCookie:
		return true
	default:
		return false
	}
}

func NormalizeResourceStatus(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ResourceStatusActive
	}
	return value
}

func IsValidResourceStatus(value string) bool {
	switch NormalizeResourceStatus(value) {
	case ResourceStatusActive, ResourceStatusInactive, ResourceStatusUsed, ResourceStatusError:
		return true
	default:
		return false
	}
}

func ResourceTypes() []string {
	return []string{ResourceTypeToken, ResourceTypeProxy, ResourceTypeCookie}
}

func ResourceStatuses() []string {
	return []string{ResourceStatusActive, ResourceStatusInactive, ResourceStatusUsed, ResourceStatusError}
}

func HashResourceValue(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

func MaskResourceValue(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) == 0 {
		return ""
	}
	if len(runes) <= 8 {
		return "********"
	}
	return string(runes[:4]) + "..." + string(runes[len(runes)-4:])
}

func (r *Resource) Normalize() {
	r.Type = NormalizeResourceType(r.Type)
	r.Status = NormalizeResourceStatus(r.Status)
	r.Value = strings.TrimSpace(r.Value)
	if r.ValueHash == "" && r.Value != "" {
		r.ValueHash = HashResourceValue(r.Value)
	}
}
