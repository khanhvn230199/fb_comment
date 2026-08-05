package controller

import (
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const maxPaginationPerPage = 500

type PaginationResponse struct {
	Page       int   `json:"page"`
	PerPage    int   `json:"per_page"`
	Limit      int   `json:"limit"`
	Offset     int   `json:"offset"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
	HasNext    bool  `json:"has_next"`
	HasPrev    bool  `json:"has_prev"`
}

func paginationFromRequest(c *gin.Context, defaultPerPage int) PaginationResponse {
	perPage := queryIntWithDefault(c, "per_page", 0)
	if perPage <= 0 {
		perPage = queryIntWithDefault(c, "limit", defaultPerPage)
	}
	if perPage <= 0 {
		perPage = defaultPerPage
	}
	if perPage > maxPaginationPerPage {
		perPage = maxPaginationPerPage
	}

	offsetValue := strings.TrimSpace(c.Query("offset"))
	if offsetValue != "" {
		offset := queryIntWithDefault(c, "offset", 0)
		if offset < 0 {
			offset = 0
		}
		return PaginationResponse{
			Page:    offset/perPage + 1,
			PerPage: perPage,
			Limit:   perPage,
			Offset:  offset,
		}
	}

	page := queryIntWithDefault(c, "page", 1)
	if page <= 0 {
		page = 1
	}
	return PaginationResponse{
		Page:    page,
		PerPage: perPage,
		Limit:   perPage,
		Offset:  (page - 1) * perPage,
	}
}

func completePagination(pagination PaginationResponse, total int64) PaginationResponse {
	pagination.Total = total
	if pagination.PerPage <= 0 {
		pagination.PerPage = 1
	}
	pagination.Limit = pagination.PerPage
	pagination.TotalPages = int(math.Ceil(float64(total) / float64(pagination.PerPage)))
	if total == 0 {
		pagination.TotalPages = 0
	}
	if pagination.Page <= 0 {
		pagination.Page = pagination.Offset/pagination.PerPage + 1
	}
	pagination.HasPrev = pagination.Offset > 0
	pagination.HasNext = int64(pagination.Offset+pagination.PerPage) < total
	return pagination
}

func paginationPageURL(c *gin.Context, page int, pagination PaginationResponse) string {
	if page < 1 {
		page = 1
	}
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		values = url.Values{}
	}
	values.Del("offset")
	values.Set("page", strconv.Itoa(page))
	values.Set("per_page", strconv.Itoa(pagination.PerPage))
	values.Del("limit")

	path := c.Request.URL.Path
	encoded := values.Encode()
	if encoded == "" {
		return path
	}
	return path + "?" + encoded
}

func paginationPrevURL(c *gin.Context, pagination PaginationResponse) string {
	if !pagination.HasPrev {
		return ""
	}
	return paginationPageURL(c, pagination.Page-1, pagination)
}

func paginationNextURL(c *gin.Context, pagination PaginationResponse) string {
	if !pagination.HasNext {
		return ""
	}
	return paginationPageURL(c, pagination.Page+1, pagination)
}

func queryIntWithDefault(c *gin.Context, key string, fallback int) int {
	value := strings.TrimSpace(c.Query(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
