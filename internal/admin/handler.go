package admin

import (
	"encoding/json"
	"io"
	"net/http"

	"modelrouter/internal/config"
	"modelrouter/internal/metrics"
	"modelrouter/internal/router"
)

type Handler struct {
	store      *router.Store
	recorder   *metrics.Recorder
	configPath string
}

func NewHandler(store *router.Store, recorder *metrics.Recorder, configPath string) *Handler {
	return &Handler{store: store, recorder: recorder, configPath: configPath}
}

func (h *Handler) Config(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, h.store.Get().Config)
	case http.MethodPut:
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
		if err != nil {
			writeAdminError(w, http.StatusBadRequest, "failed to read request body")
			return
		}
		cfg, err := config.DecodeJSON(body)
		if err != nil {
			writeAdminError(w, http.StatusBadRequest, err.Error())
			return
		}
		h.store.Update(cfg)
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) Reload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	cfg, err := config.LoadFile(h.configPath)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.store.Update(cfg)
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	stats := h.recorder.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"generated_at_unix_sec": stats.GeneratedAtUnixSec,
		"items":                 stats.Items,
		"health":                h.store.Get().Health(),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeAdminError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
