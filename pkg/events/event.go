package events

import "time"

// Event represents something that happened during the delivery pipeline.
type Event struct {
	Type      string
	Timestamp time.Time
	JobID     string
	From      string
	To        string // single recipient for per-rcpt events, empty otherwise
	LocalIP   string
	Method    string
	Attempt   int
	Err       error
	Meta      map[string]string
}
