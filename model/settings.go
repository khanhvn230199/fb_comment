package model

import "time"

const (
	DefaultCommentPollIntervalSeconds = 5
	DefaultMetricsPollIntervalSeconds = 3600
)

type PollingSettings struct {
	ID                         uint      `gorm:"primaryKey" json:"id"`
	CommentPollIntervalSeconds int       `gorm:"not null;default:5" json:"comment_poll_interval_seconds"`
	MetricsPollIntervalSeconds int       `gorm:"not null;default:3600" json:"metrics_poll_interval_seconds"`
	CreatedAt                  time.Time `json:"created_at"`
	UpdatedAt                  time.Time `json:"updated_at"`
}

func NewPollingSettings() PollingSettings {
	settings := PollingSettings{
		ID:                         1,
		CommentPollIntervalSeconds: DefaultCommentPollIntervalSeconds,
		MetricsPollIntervalSeconds: DefaultMetricsPollIntervalSeconds,
	}
	settings.Normalize()
	return settings
}

func (s *PollingSettings) Normalize() {
	if s.CommentPollIntervalSeconds < DefaultCommentPollIntervalSeconds {
		s.CommentPollIntervalSeconds = DefaultCommentPollIntervalSeconds
	}
	if s.MetricsPollIntervalSeconds < DefaultMetricsPollIntervalSeconds {
		s.MetricsPollIntervalSeconds = DefaultMetricsPollIntervalSeconds
	}
}

func (s PollingSettings) CommentNextAt(now time.Time) time.Time {
	return now.Add(time.Duration(s.CommentPollIntervalSeconds) * time.Second)
}

func (s PollingSettings) MetricsNextAt(now time.Time) time.Time {
	return now.Add(time.Duration(s.MetricsPollIntervalSeconds) * time.Second)
}

func ScheduleCommentCrawl(link *Link, settings PollingSettings, now time.Time) {
	if link == nil {
		return
	}
	next := settings.CommentNextAt(now)
	link.NextScrapeAt = next
}

func ScheduleMetricsRefresh(link *Link, settings PollingSettings, now time.Time) {
	if link == nil {
		return
	}
	next := settings.MetricsNextAt(now)
	link.MetricsNextRefreshAt = &next
}

func ScheduleAllPolling(link *Link, settings PollingSettings, now time.Time) {
	if link == nil {
		return
	}
	ScheduleCommentCrawl(link, settings, now)
	ScheduleMetricsRefresh(link, settings, now)
}
