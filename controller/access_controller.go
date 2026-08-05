package controller

import (
	"fb_comment/model"

	"gorm.io/gorm"
)

func scopedLinksQuery(user model.User) *gorm.DB {
	query := model.DB.Model(&model.Link{})
	if user.IsAdmin() {
		return query
	}
	return query.Where("links.user_id = ?", user.ID)
}

func scopedCommentsQuery(user model.User) *gorm.DB {
	query := model.DB.Model(&model.Comment{})
	if user.IsAdmin() {
		return query
	}
	return query.Joins("JOIN links ON links.id = comments.link_id").Where("links.user_id = ?", user.ID)
}

func canAccessLink(user model.User, linkID uint) (bool, error) {
	if user.IsAdmin() {
		return true, nil
	}
	var count int64
	if err := model.DB.Model(&model.Link{}).
		Where("id = ? AND user_id = ?", linkID, user.ID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func canManageLink(user model.User, link model.Link) bool {
	if user.IsAdmin() {
		return true
	}
	return link.UserID != nil && *link.UserID == user.ID
}

func canDeleteLink(user model.User) bool {
	return user.IsAdmin()
}

func scopedResourcesQuery(user model.User) *gorm.DB {
	query := model.DB.Model(&model.Resource{})
	if user.IsAdmin() {
		return query
	}
	return query.Where("resources.user_id = ?", user.ID)
}
