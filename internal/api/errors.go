package api

import "net/http"

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(mustJSON(ErrorResponse{Error: msg}))
}
