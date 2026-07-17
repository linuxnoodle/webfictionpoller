package models

import "time"

// SeriesSource is one of potentially many hosting locations for a series.
// A series has exactly one is_primary source at a time; alternates serve as
// polling/fetch fallbacks when the primary errors out.
//
// Health tracking (last_ok, last_fail, consecutive_fails, last_error) lets
// the scheduler make informed failover decisions and surfaces real-time
// status in the UI.
type SeriesSource struct {
	ID              int64      `json:"id"`
	SeriesID        int64      `json:"series_id"`
	ProviderName    string     `json:"provider_name"`
	SourceURL       string     `json:"source_url"`
	Priority        int        `json:"priority"` // lower = higher priority
	IsPrimary       bool       `json:"is_primary"`
	LastOK          *time.Time `json:"last_ok,omitempty"`
	LastFail        *time.Time `json:"last_fail,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	ConsecutiveFails int       `json:"consecutive_fails"`
	Disabled        bool       `json:"disabled"`
	CreatedAt       time.Time  `json:"created_at"`
}
