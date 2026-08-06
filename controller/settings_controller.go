package controller

import (
	"net/http"
	"strings"

	"fb_comment/model"

	"github.com/gin-gonic/gin"
)

type PollingSettingsRequest struct {
	CommentPollIntervalSeconds int `form:"comment_poll_interval_seconds" json:"comment_poll_interval_seconds"`
	MetricsPollIntervalSeconds int `form:"metrics_poll_interval_seconds" json:"metrics_poll_interval_seconds"`
}

func ShowSettings(c *gin.Context) {
	settings := model.LoadPollingSettings()
	renderSettingsPage(c, settings, c.Query("success"), c.Query("error"))
}

func UpdateSettings(c *gin.Context) {
	settings := model.LoadPollingSettings()
	settings.CommentPollIntervalSeconds = atoiWithDefault(c.PostForm("comment_poll_interval_seconds"), model.DefaultCommentPollIntervalSeconds)
	settings.MetricsPollIntervalSeconds = atoiWithDefault(c.PostForm("metrics_poll_interval_seconds"), model.DefaultMetricsPollIntervalSeconds)
	settings.Normalize()

	if err := model.DB.Save(&settings).Error; err != nil {
		renderSettingsPage(c, settings, "", "Không thể cập nhật cấu hình polling")
		return
	}

	model.RescheduleActiveLinksFromSettings(settings)
	c.Redirect(http.StatusFound, "/settings?success=updated")
}

func renderSettingsPage(c *gin.Context, settings model.PollingSettings, success string, errorMessage string) {
	c.HTML(http.StatusOK, "settings.html", gin.H{
		"currentUser": currentUser(c),
		"settings":    settings,
		"success":     strings.TrimSpace(success),
		"error":       strings.TrimSpace(errorMessage),
	})
}
