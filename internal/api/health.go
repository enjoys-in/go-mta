package api

import "net/http"

func (a *API) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(mustJSON(HealthResponse{Status: "ok"}))
}
