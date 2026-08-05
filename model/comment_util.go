package model

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var phoneCandidatePattern = regexp.MustCompile(`(?:\+?84|0)(?:[\s.\-()]*\d){8,10}`)

func ExtractFacebookUID(profileURL string) string {
	profileURL = strings.TrimSpace(profileURL)
	if profileURL == "" {
		return ""
	}

	parsed, err := url.Parse(profileURL)
	if err != nil {
		return ""
	}
	if id := strings.TrimSpace(parsed.Query().Get("id")); isDigits(id) {
		return id
	}

	for segment := range strings.SplitSeq(strings.Trim(parsed.Path, "/"), "/") {
		segment = strings.TrimSpace(segment)
		if isDigits(segment) && len(segment) >= 5 {
			return segment
		}
	}

	return ""
}

func ExtractPhone(texts ...string) string {
	for _, text := range texts {
		for _, candidate := range phoneCandidatePattern.FindAllString(text, -1) {
			phone := normalizePhone(candidate)
			if isLikelyPhone(phone) {
				return phone
			}
		}
	}
	return ""
}

func ParseFacebookTime(value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05-0700",
		"2006-01-02T15:04:05Z0700",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			parsed = parsed.UTC()
			return &parsed
		}
	}

	return nil
}

func ResolveFacebookCommentTime(anchor time.Time, value string) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	if parsed := ParseFacebookTime(value); parsed != nil {
		return parsed
	}

	if parsed := parseFacebookRelativeTime(anchor, value); parsed != nil {
		return parsed
	}

	return nil
}

func parseFacebookRelativeTime(anchor time.Time, value string) *time.Time {
	if anchor.IsZero() {
		return nil
	}

	normalized := normalizeFacebookTimeLabel(value)
	if normalized == "" {
		return nil
	}

	switch normalized {
	case "vừa xong", "just now":
		resolved := anchor.UTC()
		return &resolved
	}

	normalized = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(normalized, " trước"), " ago"))
	if normalized == "" {
		return nil
	}

	match := facebookRelativeTimePattern.FindStringSubmatch(normalized)
	if len(match) != 3 {
		return nil
	}

	amount, err := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", "."), 64)
	if err != nil || amount <= 0 {
		return nil
	}

	resolved, ok := addFacebookRelativeDuration(anchor.UTC(), amount, match[2])
	if !ok {
		return nil
	}

	return &resolved
}

func addFacebookRelativeDuration(anchor time.Time, amount float64, unit string) (time.Time, bool) {
	switch unit {
	case "giây", "giay", "second", "seconds", "sec", "secs", "s":
		return anchor.Add(-time.Duration(amount * float64(time.Second))), true
	case "phút", "phut", "p", "minute", "minutes", "min", "mins", "m":
		return anchor.Add(-time.Duration(amount * float64(time.Minute))), true
	case "giờ", "gio", "hour", "hours", "hr", "hrs", "h":
		return anchor.Add(-time.Duration(amount * float64(time.Hour))), true
	case "ngày", "ngay", "day", "days", "d":
		return anchor.Add(-time.Duration(amount * float64(24*time.Hour))), true
	case "tuần", "tuan", "week", "weeks", "w":
		return anchor.Add(-time.Duration(amount * float64(7*24*time.Hour))), true
	case "tháng", "thang", "month", "months", "mo":
		whole := int(amount)
		if amount == float64(whole) {
			return anchor.AddDate(0, -whole, 0), true
		}
		return anchor.Add(-time.Duration(amount * float64(30*24*time.Hour))), true
	case "năm", "nam", "year", "years", "y":
		whole := int(amount)
		if amount == float64(whole) {
			return anchor.AddDate(-whole, 0, 0), true
		}
		return anchor.Add(-time.Duration(amount * float64(365*24*time.Hour))), true
	default:
		return time.Time{}, false
	}
}

func normalizeFacebookTimeLabel(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		return unicode.ToLower(r)
	}, strings.TrimSpace(value))
	return strings.Join(strings.Fields(value), " ")
}

var facebookRelativeTimePattern = regexp.MustCompile(`(?i)^(\d+(?:[.,]\d+)?)\s*([\p{L}]+)$`)

func normalizePhone(value string) string {
	var builder strings.Builder
	for i, r := range value {
		if unicode.IsDigit(r) || (r == '+' && i == 0) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func isLikelyPhone(value string) bool {
	digits := strings.TrimPrefix(value, "+")
	if !isDigits(digits) {
		return false
	}
	if strings.HasPrefix(digits, "84") {
		return len(digits) >= 10 && len(digits) <= 12
	}
	if strings.HasPrefix(digits, "0") {
		return len(digits) >= 10 && len(digits) <= 11
	}
	return len(digits) >= 9 && len(digits) <= 15
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
