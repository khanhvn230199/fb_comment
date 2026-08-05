package model

import "time"

type Comment struct {
	ID                uint       `gorm:"primaryKey" json:"id"`
	LinkID            uint       `gorm:"not null;index" json:"link_id"`
	Link              Link       `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"link"`
	CommentKey        string     `gorm:"not null;size:255;index" json:"comment_key"`
	Author            string     `gorm:"size:255" json:"author"`
	AuthorUID         string     `gorm:"size:100;index" json:"author_uid"`
	Phone             string     `gorm:"size:50;index" json:"phone"`
	CommentText       string     `gorm:"column:comment_text;type:text;not null" json:"comment_text"`
	DateLabel         string     `gorm:"size:100" json:"date_label"`
	RawText           string     `gorm:"type:text" json:"raw_text"`
	ProfileURL        string     `gorm:"type:text" json:"profile_url"`
	Permalink         string     `gorm:"type:text" json:"permalink"`
	FacebookCreatedAt *time.Time `gorm:"index" json:"facebook_created_at"`
	FirstSeenAt       time.Time  `gorm:"not null;index" json:"first_seen_at"`
	ScrapedAt         time.Time  `gorm:"not null;index" json:"scraped_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}
