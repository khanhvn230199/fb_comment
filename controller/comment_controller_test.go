package controller

import (
	"testing"
	"time"
)

func TestCommentDateRangeFromValues(t *testing.T) {
	today := todayString(t)
	tests := []struct {
		name         string
		start        string
		end          string
		legacy       string
		defaultToday bool
		wantStatus   int
		wantHasDate  bool
		wantDefault  bool
		wantStart    string
		wantEnd      string
	}{
		{
			name:         "default to today when blank on page",
			defaultToday: true,
			wantHasDate:  true,
			wantDefault:  true,
			wantStart:    today,
			wantEnd:      today,
		},
		{
			name:        "no date on api stays unfiltered",
			wantHasDate: false,
		},
		{
			name:        "legacy single date still works",
			legacy:      "2026-08-06",
			wantHasDate: true,
			wantStart:   "2026-08-06",
			wantEnd:     "2026-08-06",
		},
		{
			name:        "start only becomes single day",
			start:       "2026-08-01",
			wantHasDate: true,
			wantStart:   "2026-08-01",
			wantEnd:     "2026-08-01",
		},
		{
			name:        "end only becomes single day",
			end:         "2026-08-03",
			wantHasDate: true,
			wantStart:   "2026-08-03",
			wantEnd:     "2026-08-03",
		},
		{
			name:        "explicit range is preserved",
			start:       "2026-08-01",
			end:         "2026-08-03",
			wantHasDate: true,
			wantStart:   "2026-08-01",
			wantEnd:     "2026-08-03",
		},
		{
			name:        "new params override legacy value",
			start:       "2026-08-01",
			end:         "2026-08-03",
			legacy:      "2026-08-10",
			wantHasDate: true,
			wantStart:   "2026-08-01",
			wantEnd:     "2026-08-03",
		},
		{
			name:       "invalid range is rejected",
			start:      "2026-08-03",
			end:        "2026-08-01",
			wantStatus: 400,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filters, status, message := commentDateRangeFromValues(tt.start, tt.end, tt.legacy, tt.defaultToday)
			if status != tt.wantStatus {
				t.Fatalf("status = %d, want %d (message=%q)", status, tt.wantStatus, message)
			}

			if tt.wantStatus != 0 {
				if message == "" {
					t.Fatalf("expected an error message")
				}
				return
			}

			if filters.HasDate != tt.wantHasDate {
				t.Fatalf("HasDate = %v, want %v", filters.HasDate, tt.wantHasDate)
			}
			if filters.DefaultDate != tt.wantDefault {
				t.Fatalf("DefaultDate = %v, want %v", filters.DefaultDate, tt.wantDefault)
			}
			if !tt.wantHasDate {
				if filters.StartDate != "" || filters.EndDate != "" {
					t.Fatalf("unexpected dates: start=%q end=%q", filters.StartDate, filters.EndDate)
				}
				if !filters.DateStart.IsZero() || !filters.DateEnd.IsZero() {
					t.Fatalf("unexpected date bounds: start=%v end=%v", filters.DateStart, filters.DateEnd)
				}
				return
			}

			if filters.StartDate != tt.wantStart {
				t.Fatalf("StartDate = %q, want %q", filters.StartDate, tt.wantStart)
			}
			if filters.EndDate != tt.wantEnd {
				t.Fatalf("EndDate = %q, want %q", filters.EndDate, tt.wantEnd)
			}

			startDay := mustParseLocalDay(t, tt.wantStart)
			endDay := mustParseLocalDay(t, tt.wantEnd)
			if !filters.DateStart.Equal(startDay.UTC()) {
				t.Fatalf("DateStart = %v, want %v", filters.DateStart, startDay.UTC())
			}
			if !filters.DateEnd.Equal(endDay.AddDate(0, 0, 1).UTC()) {
				t.Fatalf("DateEnd = %v, want %v", filters.DateEnd, endDay.AddDate(0, 0, 1).UTC())
			}
		})
	}
}

func todayString(t *testing.T) string {
	t.Helper()
	return time.Now().In(time.Local).Format(commentDateLayout)
}

func mustParseLocalDay(t *testing.T, value string) time.Time {
	t.Helper()
	day, err := time.ParseInLocation(commentDateLayout, value, time.Local)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return day
}
