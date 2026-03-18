package api

import "encoding/json"

// RecipientResult represents the queued status of a single recipient.
type RecipientResult struct {
	ID     string `json:"id"`
	To     string `json:"to"`
	Status string `json:"status"`
}

// SendResponse is the JSON response for POST /api/v1/send.
type SendResponse struct {
	Recipients []RecipientResult `json:"recipients"`
}

// ErrorResponse is the JSON error envelope.
type ErrorResponse struct {
	Error string `json:"error"`
}

// HealthResponse is the JSON response for GET /api/v1/health.
type HealthResponse struct {
	Status string `json:"status"`
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
