package admin

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"modelrouter/internal/config"
	"modelrouter/internal/metrics"
	"modelrouter/internal/router"
	"modelrouter/internal/usage"
)

type ClientLimitProvider interface {
	ClientLimitStatus() any
}

type Handler struct {
	store               *router.Store
	recorder            *metrics.Recorder
	configPath          string
	clientLimitProvider ClientLimitProvider
}

func NewHandler(store *router.Store, recorder *metrics.Recorder, configPath string) *Handler {
	return &Handler{store: store, recorder: recorder, configPath: configPath}
}

func (h *Handler) WithClientLimitProvider(provider ClientLimitProvider) *Handler {
	h.clientLimitProvider = provider
	return h
}

func (h *Handler) Config(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !h.authorize(w, r, config.AdminPermissionConfigRead) {
			return
		}
		writeJSON(w, http.StatusOK, redactedConfig(h.store.Get().Config))
	case http.MethodPut:
		if !h.authorize(w, r, config.AdminPermissionConfigWrite) {
			return
		}
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
	if !h.authorize(w, r, config.AdminPermissionConfigWrite) {
		return
	}
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
	if !h.authorize(w, r, config.AdminPermissionRead) {
		return
	}
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
		"limits":                h.clientLimits(),
	})
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r, config.AdminPermissionHealthRead) {
		return
	}
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, h.store.Get().Health())
}

func (h *Handler) Limits(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r, config.AdminPermissionLimitsRead) {
		return
	}
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, h.clientLimits())
}

func (h *Handler) Metrics(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r, config.AdminPermissionMetricsRead) {
		return
	}
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

func (h *Handler) Usage(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r, config.AdminPermissionMetricsRead) {
		return
	}
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/admin/usage")
	switch path {
	case "", "/", "/recent", "/summary":
	default:
		writeAdminError(w, http.StatusNotFound, "not found")
		return
	}
	query := usageQueryFromRequest(r)
	if path == "/summary" {
		result, err := usage.AggregateRecords(h.store.Get().Config.UsageLog, query, r.URL.Query().Get("interval"), boundedInt(r.URL.Query().Get("top"), 10, 1, 100))
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"meta":   usagePageMeta(0, query, 0),
			"result": result,
		})
		return
	}
	result, err := usage.QueryRecords(h.store.Get().Config.UsageLog, query)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"meta":  usagePageMeta(result.Total, query, len(result.Items)),
		"items": result.Items,
	})
}

type metricsQuery struct {
	Client     string `json:"client,omitempty"`
	Model      string `json:"model,omitempty"`
	RouteGroup string `json:"route_group,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
}

func usageQueryFromRequest(r *http.Request) usage.Query {
	values := r.URL.Query()
	limitDefault := 100
	if strings.TrimPrefix(r.URL.Path, "/admin/usage") == "/recent" {
		limitDefault = 100
	}
	return usage.Query{
		Client:     values.Get("client"),
		Model:      values.Get("model"),
		RouteGroup: values.Get("route_group"),
		Endpoint:   values.Get("endpoint"),
		FromUnix:   boundedInt64(values.Get("from"), 0, 0),
		ToUnix:     boundedInt64(values.Get("to"), 0, 0),
		Limit:      boundedInt(values.Get("limit"), limitDefault, 0, 1000),
		Offset:     boundedInt(values.Get("offset"), 0, 0, 1_000_000),
	}
}

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, permission string) bool {
	adminConfig := h.store.Get().Config.Admin
	token := strings.TrimSpace(adminConfig.Token)
	if token == "" && len(adminConfig.Keys) == 0 {
		return true
	}
	got, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="modelrouter-admin"`)
		writeAdminError(w, http.StatusUnauthorized, "invalid or missing admin token")
		return false
	}
	if token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1 {
		return true
	}
	for _, key := range adminConfig.Keys {
		if subtle.ConstantTimeCompare([]byte(got), []byte(key.Key)) != 1 {
			continue
		}
		if adminKeyAllows(key, permission) {
			return true
		}
		writeAdminError(w, http.StatusForbidden, "admin token does not have required permission")
		return false
	}
	writeAdminError(w, http.StatusUnauthorized, "invalid or missing admin token")
	return false
}

func adminKeyAllows(key config.AdminKeyConfig, permission string) bool {
	for _, candidate := range key.Permissions {
		switch candidate {
		case config.AdminPermissionAll:
			return true
		case config.AdminPermissionRead:
			if adminReadPermission(permission) {
				return true
			}
		case config.AdminPermissionWrite:
			if adminWritePermission(permission) {
				return true
			}
		case permission:
			return true
		}
	}
	return false
}

func adminReadPermission(permission string) bool {
	switch permission {
	case config.AdminPermissionConfigRead,
		config.AdminPermissionMetricsRead,
		config.AdminPermissionHealthRead,
		config.AdminPermissionLimitsRead,
		config.AdminPermissionRead:
		return true
	default:
		return false
	}
}

func adminWritePermission(permission string) bool {
	return permission == config.AdminPermissionConfigWrite
}

func bearerToken(header string) (string, bool) {
	if strings.TrimSpace(header) == "" {
		return "", false
	}
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	return parts[1], true
}

func redactedConfig(cfg *config.Config) *config.Config {
	if cfg == nil {
		return nil
	}
	clone := *cfg
	if clone.Admin.Token != "" {
		clone.Admin.Token = "********"
	}
	clone.Admin.Keys = append([]config.AdminKeyConfig(nil), cfg.Admin.Keys...)
	for i := range clone.Admin.Keys {
		if clone.Admin.Keys[i].Key != "" {
			clone.Admin.Keys[i].Key = "********"
		}
		clone.Admin.Keys[i].Permissions = append([]string(nil), clone.Admin.Keys[i].Permissions...)
	}
	clone.Auth.Keys = append([]config.ClientKeyConfig(nil), cfg.Auth.Keys...)
	for i := range clone.Auth.Keys {
		if clone.Auth.Keys[i].Key != "" {
			clone.Auth.Keys[i].Key = "********"
		}
	}
	clone.Models = make(map[string]config.ModelConfig, len(cfg.Models))
	for key, value := range cfg.Models {
		clone.Models[key] = value
	}
	clone.AccessGroups = make(map[string]config.AccessGroupConfig, len(cfg.AccessGroups))
	for key, value := range cfg.AccessGroups {
		value.AllowedModels = append([]string(nil), value.AllowedModels...)
		value.BlockedModels = append([]string(nil), value.BlockedModels...)
		clone.AccessGroups[key] = value
	}
	clone.RouteGroups = make(map[string]config.RouteGroupConfig, len(cfg.RouteGroups))
	for key, value := range cfg.RouteGroups {
		value.Endpoints = append([]config.EndpointConfig(nil), value.Endpoints...)
		for i := range value.Endpoints {
			if value.Endpoints[i].APIKey != "" {
				value.Endpoints[i].APIKey = "********"
			}
		}
		clone.RouteGroups[key] = value
	}
	return &clone
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

func boundedInt64(raw string, fallback, min int64) int64 {
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fallback
	}
	if parsed < min {
		return min
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

func usagePageMeta(total int, query usage.Query, returned int) map[string]any {
	return map[string]any{
		"total":    total,
		"returned": returned,
		"limit":    query.Limit,
		"offset":   query.Offset,
		"filters": map[string]any{
			"client":      query.Client,
			"model":       query.Model,
			"route_group": query.RouteGroup,
			"endpoint":    query.Endpoint,
			"from":        query.FromUnix,
			"to":          query.ToUnix,
		},
	}
}

func (h *Handler) clientLimits() any {
	if h.clientLimitProvider == nil {
		return nil
	}
	return h.clientLimitProvider.ClientLimitStatus()
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
