// Package dsn generates RFC 3464 Delivery Status Notifications.
package dsn

import (
	"fmt"
	"strings"
	"time"
)

// Action describes the disposition taken by the MTA.
type Action string

const (
	ActionFailed    Action = "failed"
	ActionDelayed   Action = "delayed"
	ActionDelivered Action = "delivered"
	ActionRelayed   Action = "relayed"
	ActionExpanded  Action = "expanded"
)

// RecipientStatus holds per-recipient delivery status fields.
type RecipientStatus struct {
	FinalRecipient string // e.g. "rfc822;user@example.com"
	Action         Action
	StatusCode     string // e.g. "5.1.1"
	DiagnosticCode string // e.g. "smtp;550 No such user"
	RemoteMTA      string // e.g. "dns;mx.example.com"
}

// Report represents a full DSN message.
type Report struct {
	// Envelope
	EnvelopeFrom string // original sender (for bounce return path)
	ReportingMTA string // e.g. "dns;mail.myserver.com"
	ArrivalDate  time.Time

	// Per-recipient fields
	Recipients []RecipientStatus

	// Original message (optional, first N bytes for inclusion)
	OriginalHeaders string
}

// Build generates the multipart/report DSN message body as defined in RFC 3464.
// Returns the raw message body and the Content-Type header value.
func Build(r *Report) (body string, contentType string) {
	boundary := "=_gomta_dsn_" + fmt.Sprintf("%d", r.ArrivalDate.UnixNano())

	contentType = fmt.Sprintf("multipart/report; report-type=delivery-status; boundary=%q", boundary)

	var sb strings.Builder

	// Part 1: human-readable explanation
	sb.WriteString("--")
	sb.WriteString(boundary)
	sb.WriteString("\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
	sb.WriteString("This is an automatically generated Delivery Status Notification.\r\n\r\n")

	for _, rs := range r.Recipients {
		rcpt := rs.FinalRecipient
		if idx := strings.Index(rcpt, ";"); idx >= 0 {
			rcpt = rcpt[idx+1:]
		}
		switch rs.Action {
		case ActionFailed:
			sb.WriteString(fmt.Sprintf("Delivery to <%s> failed permanently.\r\nReason: %s\r\n\r\n", rcpt, rs.DiagnosticCode))
		case ActionDelayed:
			sb.WriteString(fmt.Sprintf("Delivery to <%s> has been delayed.\r\nReason: %s\r\n\r\n", rcpt, rs.DiagnosticCode))
		default:
			sb.WriteString(fmt.Sprintf("Delivery to <%s>: %s\r\n\r\n", rcpt, rs.Action))
		}
	}

	// Part 2: message/delivery-status
	sb.WriteString("--")
	sb.WriteString(boundary)
	sb.WriteString("\r\nContent-Type: message/delivery-status\r\n\r\n")

	// Per-message fields
	sb.WriteString(fmt.Sprintf("Reporting-MTA: %s\r\n", r.ReportingMTA))
	if !r.ArrivalDate.IsZero() {
		sb.WriteString(fmt.Sprintf("Arrival-Date: %s\r\n", r.ArrivalDate.UTC().Format(time.RFC1123Z)))
	}
	sb.WriteString("\r\n")

	// Per-recipient fields
	for _, rs := range r.Recipients {
		sb.WriteString(fmt.Sprintf("Final-Recipient: %s\r\n", rs.FinalRecipient))
		sb.WriteString(fmt.Sprintf("Action: %s\r\n", rs.Action))
		sb.WriteString(fmt.Sprintf("Status: %s\r\n", rs.StatusCode))
		if rs.DiagnosticCode != "" {
			sb.WriteString(fmt.Sprintf("Diagnostic-Code: %s\r\n", rs.DiagnosticCode))
		}
		if rs.RemoteMTA != "" {
			sb.WriteString(fmt.Sprintf("Remote-MTA: %s\r\n", rs.RemoteMTA))
		}
		sb.WriteString("\r\n")
	}

	// Part 3: original headers (optional)
	if r.OriginalHeaders != "" {
		sb.WriteString("--")
		sb.WriteString(boundary)
		sb.WriteString("\r\nContent-Type: text/rfc822-headers\r\n\r\n")
		sb.WriteString(r.OriginalHeaders)
		sb.WriteString("\r\n")
	}

	// Closing boundary
	sb.WriteString("--")
	sb.WriteString(boundary)
	sb.WriteString("--\r\n")

	return sb.String(), contentType
}

// StatusCodeFromSMTP converts a 3-digit SMTP code to an RFC 3464 status code.
// e.g. 550 → "5.0.0", 421 → "4.0.0"
func StatusCodeFromSMTP(code int) string {
	class := code / 100
	return fmt.Sprintf("%d.0.0", class)
}
