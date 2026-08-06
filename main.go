package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"fb_comment/controller"
	"fb_comment/model"
	"fb_comment/poller"
	"fb_comment/scraper"

	"github.com/gin-gonic/gin"
)

func main() {
	model.InitDatabase()
	model.SeedDefaultAdmin()
	model.EnsureResourceOwnership()
	settings, created := model.EnsurePollingSettings()
	if created {
		model.RescheduleActiveLinksFromSettings(settings)
	}

	commentScraper := scraper.NewAPIScraper(scraper.ConfigFromEnv())
	if err := commentScraper.Start(); err != nil {
		log.Fatalf("cannot start Playwright scraper: %v", err)
	}
	defer commentScraper.Close()

	metricsUpdater := controller.NewFacebookMetricsUpdater(model.DB, log.Default())
	pollerCtx, stopPoller := context.WithCancel(context.Background())
	defer stopPoller()
	linkPoller := poller.NewLinkPoller(model.DB, commentScraper, log.Default())
	go linkPoller.Start(pollerCtx)

	metricsPoller := poller.NewMetricsPoller(model.DB, metricsUpdater, log.Default())
	go metricsPoller.Start(pollerCtx)

	router := gin.Default()
	router.LoadHTMLGlob("view/*.html")
	router.GET("/healthz", healthz)

	controller.RegisterRoutes(router, commentScraper)

	port := getEnv("APP_PORT", "8080")
	log.Printf("fb_comment is running at http://localhost:%s", port)
	if err := router.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}

func healthz(c *gin.Context) {
	sqlDB, err := model.DB.DB()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "database": "unavailable"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	if err := sqlDB.PingContext(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "database": "down"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func getEnv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
