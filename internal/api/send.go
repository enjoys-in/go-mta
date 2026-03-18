package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

// handleSend accepts a raw RFC 5322 message via POST.
//
//	Content-Type: message/rfc5322
//	X-Mail-From:  sender@yourdomain.com
//	X-Recipients: recipient@example.com, recipient2@example.com
//	Body:         raw .eml data
func (a *API) handleSend(w http.ResponseWriter, r *http.Request) {
	from := strings.TrimSpace(r.Header.Get("X-Mail-From"))
	if from == "" {
		writeError(w, http.StatusBadRequest, "missing X-Mail-From header")
		return
	}

	raw := r.Header.Get("X-Recipients")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "missing X-Recipients header")
		return
	}
	to := parseRecipients(raw)
	if len(to) == 0 {
		writeError(w, http.StatusBadRequest, "no valid recipients in X-Recipients")
		return
	}

	const maxBody = 25 << 20 // 25 MB
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "empty message body")
		return
	}

	// Submit one job per recipient so each gets its own tracking ID.
	results := make([]RecipientResult, 0, len(to))
	for i, rcpt := range to {
		jobID, err := a.srv.Submit(from, []string{rcpt}, body)
		if err != nil {
			results = append(results, RecipientResult{
				ID:     fmt.Sprintf("err_%d", i),
				To:     rcpt,
				Status: "rejected: " + err.Error(),
			})
			continue
		}
		results = append(results, RecipientResult{
			ID:     jobID,
			To:     rcpt,
			Status: "queued",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write(mustJSON(SendResponse{Recipients: results}))
}

func parseRecipients(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s != "" && strings.Contains(s, "@") {
			out = append(out, s)
		}
	}
	return out
}
