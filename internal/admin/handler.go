package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

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

func (h *Handler) Overview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	stats := h.recorder.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"generated_at_unix_sec": stats.GeneratedAtUnixSec,
		"summary":               stats.Summary,
		"windows":               stats.Windows,
		"health":                h.store.Get().Health(),
	})
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, h.store.Get().Health())
}

func (h *Handler) Metrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	stats := h.recorder.Snapshot()
	query := metricsQueryFromRequest(r)
	items := filterCounters(stats.Items, query)
	clients := filterCounters(stats.ByClient, query)
	models := filterCounters(stats.ByModel, query)
	endpoints := filterCounters(stats.ByEndpoint, query)
	recent := filterRecent(stats.Recent, query)

	switch strings.TrimPrefix(r.URL.Path, "/admin/metrics") {
	case "", "/":
		paged := paginateCounters(items, query)
		writeJSON(w, http.StatusOK, map[string]any{
			"generated_at_unix_sec": stats.GeneratedAtUnixSec,
			"summary":               stats.Summary,
			"windows":               stats.Windows,
			"meta":                  pageMeta(len(items), query, len(paged)),
			"items":                 paged,
		})
	case "/summary":
		writeJSON(w, http.StatusOK, map[string]any{
			"generated_at_unix_sec": stats.GeneratedAtUnixSec,
			"summary":               stats.Summary,
			"windows":               stats.Windows,
		})
	case "/clients":
		paged := paginateCounters(clients, query)
		writeJSON(w, http.StatusOK, map[string]any{"meta": pageMeta(len(clients), query, len(paged)), "items": paged})
	case "/models":
		paged := paginateCounters(models, query)
		writeJSON(w, http.StatusOK, map[string]any{"meta": pageMeta(len(models), query, len(paged)), "items": paged})
	case "/endpoints":
		paged := paginateCounters(endpoints, query)
		writeJSON(w, http.StatusOK, map[string]any{"meta": pageMeta(len(endpoints), query, len(paged)), "items": paged})
	case "/recent":
		paged := limitRecent(recent, query.Limit)
		writeJSON(w, http.StatusOK, map[string]any{"meta": pageMeta(len(recent), query, len(paged)), "items": paged})
	default:
		writeAdminError(w, http.StatusNotFound, "not found")
	}
}

type metricsQuery struct {
	Client     string `json:"client,omitempty"`
	Model      string `json:"model,omitempty"`
	RouteGroup string `json:"route_group,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
}

func metricsQueryFromRequest(r *http.Request) metricsQuery {
	values := r.URL.Query()
	return metricsQuery{
		Client:     values.Get("client"),
		Model:      values.Get("model"),
		RouteGroup: values.Get("route_group"),
		Endpoint:   values.Get("endpoint"),
		Limit:      boundedInt(values.Get("limit"), 100, 0, 1000),
		Offset:     boundedInt(values.Get("offset"), 0, 0, 1_000_000),
	}
}

func boundedInt(raw string, fallback, min, max int) int {
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	if parsed < min {
		return min
	}
	if parsed > max {
		return max
	}
	return parsed
}

func filterCounters(items []metrics.Counter, query metricsQuery) []metrics.Counter {
	out := make([]metrics.Counter, 0, len(items))
	for _, item := range items {
		if query.Client != "" && item.Client != query.Client {
			continue
		}
		if query.Model != "" && item.Model != query.Model {
			continue
		}
		if query.RouteGroup != "" && item.RouteGroup != query.RouteGroup {
			continue
		}
		if query.Endpoint != "" && item.Endpoint != query.Endpoint {
			continue
		}
		out = append(out, item)
	}
	return out
}

func filterRecent(items []metrics.EventRecord, query metricsQuery) []metrics.EventRecord {
	out := make([]metrics.EventRecord, 0, len(items))
	for _, item := range items {
		if query.Client != "" && item.Client != query.Client {
			continue
		}
		if query.Model != "" && item.Model != query.Model {
			continue
		}
		if query.RouteGroup != "" && item.RouteGroup != query.RouteGroup {
			continue
		}
		if query.Endpoint != "" && item.Endpoint != query.Endpoint {
			continue
		}
		out = append(out, item)
	}
	return out
}

func paginateCounters(items []metrics.Counter, query metricsQuery) []metrics.Counter {
	if query.Offset >= len(items) || query.Limit == 0 {
		return nil
	}
	end := query.Offset + query.Limit
	if end > len(items) {
		end = len(items)
	}
	return items[query.Offset:end]
}

func limitRecent(items []metrics.EventRecord, limit int) []metrics.EventRecord {
	if limit == 0 {
		return nil
	}
	if len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

func pageMeta(total int, query metricsQuery, returned int) map[string]any {
	return map[string]any{
		"total":    total,
		"returned": returned,
		"limit":    query.Limit,
		"offset":   query.Offset,
		"filters": map[string]string{
			"client":      query.Client,
			"model":       query.Model,
			"route_group": query.RouteGroup,
			"endpoint":    query.Endpoint,
		},
	}
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
