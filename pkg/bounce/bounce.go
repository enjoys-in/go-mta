package bounce

import (
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/enjoys-in/go-mta/pkg/logger"
	"github.com/enjoys-in/go-mta/pkg/types"
)

var smtpCodeRe = regexp.MustCompile(`\b([245]\d{2})\b`)

// Info describes a parsed bounce.
type Info struct {
	Code     int
	Class    int    // 2, 4, or 5
	Category string // types.BounceHard or types.BounceSoft
	Message  string
}

// Detector classifies delivery errors as hard or soft bounces.
type Detector struct {
	log *logger.Logger
}

// NewDetector creates a bounce detector.
func NewDetector() *Detector {
	return &Detector{log: logger.New(types.ComponentBounce)}
}

// Classify examines an error string and returns bounce info.
// Returns nil if the error doesn't look like an SMTP bounce.
func (d *Detector) Classify(err error) *Info {
	if err == nil {
		return nil
	}
	msg := err.Error()
	matches := smtpCodeRe.FindStringSubmatch(msg)
	if len(matches) < 2 {
		return nil
	}

	code, _ := strconv.Atoi(matches[1])
	class := code / 100

	var category string
	switch class {
	case types.SMTPClassPermanent:
		category = types.BounceHard
	case types.SMTPClassTransient:
		category = types.BounceSoft
	default:
		return nil // 2xx is not a bounce
	}

	info := &Info{
		Code:     code,
		Class:    class,
		Category: category,
		Message:  msg,
	}

	d.log.Info("bounce classified",
		slog.Int("code", code),
		slog.String("category", category),
		slog.String("message", truncate(msg, 120)),
	)

	return info
}

// IsHard returns true if the error is a permanent (5xx) failure.
func (d *Detector) IsHard(err error) bool {
	info := d.Classify(err)
	return info != nil && info.Category == types.BounceHard
}

// IsSoft returns true if the error is a transient (4xx) failure.
func (d *Detector) IsSoft(err error) bool {
	info := d.Classify(err)
	return info != nil && info.Category == types.BounceSoft
}

// ParsePerRecipient splits a multi-recipient error string into per-address results.
func ParsePerRecipient(err error) []types.RcptResult {
	if err == nil {
		return nil
	}
	msg := err.Error()
	var results []types.RcptResult

	// Look for patterns like "RCPT TO <addr>: 550 ..."
	for _, seg := range strings.Split(msg, ";") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		rr := types.RcptResult{Msg: seg}

		matches := smtpCodeRe.FindStringSubmatch(seg)
		if len(matches) >= 2 {
			rr.Code, _ = strconv.Atoi(matches[1])
		}

		// Try to extract email from <addr>
		if start := strings.Index(seg, "<"); start != -1 {
			if end := strings.Index(seg[start:], ">"); end != -1 {
				rr.Address = seg[start+1 : start+end]
			}
		}

		rr.Err = err
		results = append(results, rr)
	}
	return results
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
