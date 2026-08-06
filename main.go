package main

import (
	"context"
	"log"
	"os"

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

	controller.RegisterRoutes(router, commentScraper)

	port := getEnv("APP_PORT", "8080")
	log.Printf("fb_comment is running at http://localhost:%s", port)
	if err := router.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}

func getEnv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
