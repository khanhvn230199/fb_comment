package poller

import (
	"context"
	"log"
	"time"

	"fb_comment/model"

	"gorm.io/gorm"
)

const defaultMetricsSweepInterval = 1 * time.Second

type LinkMetricsUpdater interface {
	RefreshLinkMetrics(ctx context.Context, link *model.Link, finalURL string) error
}

type MetricsPoller struct {
	db             *gorm.DB
	metricsUpdater LinkMetricsUpdater
	logger         *log.Logger
	sweepInterval  time.Duration
}

func NewMetricsPoller(db *gorm.DB, metricsUpdater LinkMetricsUpdater, logger *log.Logger) *MetricsPoller {
	if logger == nil {
		logger = log.Default()
	}
	return &MetricsPoller{
		db:             db,
		metricsUpdater: metricsUpdater,
		logger:         logger,
		sweepInterval:  defaultMetricsSweepInterval,
	}
}

func (p *MetricsPoller) Start(ctx context.Context) {
	if p == nil || p.db == nil || p.metricsUpdater == nil {
		return
	}

	ticker := time.NewTicker(p.sweepInterval)
	defer ticker.Stop()

	p.logger.Printf("metrics poller started: sweeping due metrics every %s", p.sweepInterval)
	p.refreshDueOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			p.logger.Printf("metrics poller stopped")
			return
		case <-ticker.C:
			p.refreshDueOnce(ctx)
		}
	}
}

func (p *MetricsPoller) refreshDueOnce(ctx context.Context) {
	settings := model.LoadPollingSettings()
	now := time.Now().UTC()
	var links []model.Link
	if err := p.db.Where("active = ? AND (metrics_next_refresh_at IS NULL OR metrics_next_refresh_at <= ?)", true, now).
		Order("metrics_next_refresh_at ASC, id ASC").
		Find(&links).Error; err != nil {
		p.logger.Printf("metrics poller: cannot list due links: %v", err)
		return
	}
	if len(links) == 0 {
		return
	}

	p.logger.Printf("metrics poller: refreshing metrics for %d due link(s)", len(links))
	for i := range links {
		if err := ctx.Err(); err != nil {
			return
		}

		link := &links[i]
		finalURL := link.FinalURL
		if finalURL == "" {
			finalURL = link.URL
		}
		if err := p.metricsUpdater.RefreshLinkMetrics(ctx, link, finalURL); err != nil {
			p.logger.Printf("metrics poller: cannot update metrics for link %d: %v", link.ID, err)
		}

		p.scheduleNextRefresh(link, settings, now)
	}
}

func (p *MetricsPoller) scheduleNextRefresh(link *model.Link, settings model.PollingSettings, now time.Time) {
	model.ScheduleMetricsRefresh(link, settings, now)
	if err := p.db.Model(&model.Link{}).Where("id = ?", link.ID).Update("metrics_next_refresh_at", link.MetricsNextRefreshAt).Error; err != nil {
		p.logger.Printf("metrics poller: cannot update next metrics refresh for link %d: %v", link.ID, err)
	}
}
