package proxy

import (
	"encoding/json"
	"net/http"
)

type openAIError struct {
	Error openAIErrorBody `json:"error"`
}

type openAIErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func writeOpenAIError(w http.ResponseWriter, status int, message, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(openAIError{
		Error: openAIErrorBody{
			Message: message,
			Type:    "invalid_request_error",
			Code:    code,
		},
	})
}

func writeUnauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="modelrouter"`)
	writeOpenAIError(w, http.StatusUnauthorized, message, "invalid_api_key")
}
