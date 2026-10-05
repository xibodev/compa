package api

import (
	"encoding/json"
	"net/http"
)

// writeJSONError answers with status and {"error": message}, the error shape
// the dashboard reads from every API.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
