package proxy

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

type modelsResponse struct {
	Object string      `json:"object"`
	Data   []modelItem `json:"data"`
}

type modelItem struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func (h *Handler) Models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed", "method_not_allowed")
		return
	}
	client, ok := h.authenticate(r)
	if !ok {
		writeUnauthorized(w, "invalid or missing API key")
		return
	}

	cfg := h.store.Get().Config
	names := visibleModels(cfg, client)
	sort.Strings(names)

	now := time.Now().Unix()
	resp := modelsResponse{
		Object: "list",
		Data:   make([]modelItem, 0, len(names)),
	}
	for _, name := range names {
		resp.Data = append(resp.Data, modelItem{
			ID:      name,
			Object:  "model",
			Created: now,
			OwnedBy: "modelrouter",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
