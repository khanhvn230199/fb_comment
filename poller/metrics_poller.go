package poller

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"fb_comment/model"

	"gorm.io/gorm"
)

const defaultMetricsInterval = time.Hour

type LinkMetricsUpdater interface {
	RefreshLinkMetrics(ctx context.Context, link *model.Link, finalURL string) error
}

type MetricsPoller struct {
	db             *gorm.DB
	metricsUpdater LinkMetricsUpdater
	logger         *log.Logger
	interval       time.Duration
}

func NewMetricsPoller(db *gorm.DB, metricsUpdater LinkMetricsUpdater, logger *log.Logger) *MetricsPoller {
	if logger == nil {
		logger = log.Default()
	}
	return &MetricsPoller{
		db:             db,
		metricsUpdater: metricsUpdater,
		logger:         logger,
		interval:       metricsIntervalFromEnv(),
	}
}

func metricsIntervalFromEnv() time.Duration {
	value := strings.TrimSpace(os.Getenv("METRICS_POLL_INTERVAL"))
	if value == "" {
		return defaultMetricsInterval
	}

	if duration, err := time.ParseDuration(value); err == nil && duration > 0 {
		return duration
	}

	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}

	return defaultMetricsInterval
}

func (p *MetricsPoller) Start(ctx context.Context) {
	if p == nil || p.db == nil || p.metricsUpdater == nil {
		return
	}

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	p.logger.Printf("metrics poller started: refreshing link metrics every %s", p.interval)
	p.refreshOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			p.logger.Printf("metrics poller stopped")
			return
		case <-ticker.C:
			p.refreshOnce(ctx)
		}
	}
}

func (p *MetricsPoller) refreshOnce(ctx context.Context) {
	var links []model.Link
	if err := p.db.Where("active = ?", true).Order("id ASC").Find(&links).Error; err != nil {
		p.logger.Printf("metrics poller: cannot list active links: %v", err)
		return
	}
	if len(links) == 0 {
		return
	}

	p.logger.Printf("metrics poller: refreshing metrics for %d active link(s)", len(links))
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
	}
}
