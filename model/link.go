package model

import (
	"strings"
	"time"
)

type Link struct {
	ID                   uint       `gorm:"primaryKey" json:"id"`
	Title                string     `gorm:"size:255" json:"title"`
	URL                  string     `gorm:"uniqueIndex;not null;type:text" json:"url"`
	FinalURL             string     `gorm:"type:text" json:"final_url"`
	UserID               *uint      `gorm:"index" json:"user_id"`
	User                 User       `gorm:"constraint:OnUpdate:CASCADE,OnDelete:SET NULL;" json:"-"`
	Active               bool       `gorm:"not null;default:true" json:"active"`
	Status               string     `gorm:"not null;size:30;default:pending" json:"status"`
	LastError            string     `gorm:"type:text" json:"last_error"`
	TotalCommentCount    int64      `gorm:"not null;default:0" json:"total_comment_count"`
	TotalLikeCount       int64      `gorm:"not null;default:0" json:"total_like_count"`
	PreviousCommentCount int64      `gorm:"not null;default:0" json:"previous_comment_count"`
	PreviousLikeCount    int64      `gorm:"not null;default:0" json:"previous_like_count"`
	PreviousMetricsAt    *time.Time `gorm:"index" json:"previous_metrics_at"`
	MetricsFetchedAt     *time.Time `gorm:"index" json:"metrics_fetched_at"`
	MaxComments          int        `gorm:"not null;default:50" json:"max_comments"`
	IdlePasses           int        `gorm:"not null;default:2" json:"idle_passes"`
	MaxScrolls           int        `gorm:"not null;default:20" json:"max_scrolls"`
	LastScrapedAt        *time.Time `json:"last_scraped_at"`
	NextScrapeAt         time.Time  `gorm:"not null;index" json:"next_scrape_at"`
	MetricsNextRefreshAt *time.Time `gorm:"index" json:"metrics_next_refresh_at"`
	Comments             []Comment  `gorm:"constraint:OnDelete:CASCADE;" json:"comments,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

func NewLink(url string) Link {
	now := time.Now()
	metricsNow := now
	return Link{
		URL:                  url,
		Active:               true,
		Status:               "pending",
		MaxComments:          50,
		IdlePasses:           2,
		MaxScrolls:           20,
		NextScrapeAt:         now,
		MetricsNextRefreshAt: &metricsNow,
	}
}

func (l Link) CommentGrowth() int64 {
	if l.PreviousMetricsAt == nil {
		return 0
	}
	return l.TotalCommentCount - l.PreviousCommentCount
}

func (l Link) LikeGrowth() int64 {
	if l.PreviousMetricsAt == nil {
		return 0
	}
	return l.TotalLikeCount - l.PreviousLikeCount
}

func (l Link) HasMetricsGrowth() bool {
	return l.MetricsFetchedAt != nil && l.PreviousMetricsAt != nil
}

func (l *Link) Normalize() {
	l.Title = strings.TrimSpace(l.Title)
	if l.Status == "" {
		l.Status = "pending"
	}
	if l.MaxComments < 50 {
		l.MaxComments = 50
	}
	if l.MaxComments > 500 {
		l.MaxComments = 500
	}
	if l.MaxScrolls < 20 {
		l.MaxScrolls = 20
	}
	if l.MaxScrolls > 50 {
		l.MaxScrolls = 50
	}
	if l.IdlePasses <= 0 {
		l.IdlePasses = 2
	}
	if l.IdlePasses > 10 {
		l.IdlePasses = 10
	}
	if l.NextScrapeAt.IsZero() {
		l.NextScrapeAt = time.Now()
	}
	if l.MetricsNextRefreshAt == nil || l.MetricsNextRefreshAt.IsZero() {
		now := time.Now()
		l.MetricsNextRefreshAt = &now
	}
}
