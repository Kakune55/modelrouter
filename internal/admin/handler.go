package admin

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
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
		if err := config.SaveFile(h.configPath, cfg); err != nil {
			writeAdminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.store.Update(cfg)
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "persisted": "true"})
	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) Models(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r, configPermissionForMethod(r.Method)) {
		return
	}
	name, hasName, ok := resourceName(r.URL.EscapedPath(), "/admin/models")
	if !ok {
		writeAdminError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if hasName {
			snap := h.store.Get().Config
			model, ok := snap.Models[name]
			if !ok {
				writeAdminError(w, http.StatusNotFound, "model not found")
				return
			}
			writeJSON(w, http.StatusOK, model)
			return
		}
		writeJSON(w, http.StatusOK, h.store.Get().Config.Models)
	case http.MethodPut:
		if !hasName {
			writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var model config.ModelConfig
		if !decodeAdminJSON(w, r, &model) {
			return
		}
		if err := h.updateConfig(func(cfg *config.Config) {
			if cfg.Models == nil {
				cfg.Models = map[string]config.ModelConfig{}
			}
			cfg.Models[name] = model
		}); err != nil {
			writeAdminError(w, statusForConfigError(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "name": name, "persisted": "true"})
	case http.MethodDelete:
		if !hasName {
			writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if _, ok := h.store.Get().Config.Models[name]; !ok {
			writeAdminError(w, http.StatusNotFound, "model not found")
			return
		}
		if err := h.updateConfig(func(cfg *config.Config) {
			delete(cfg.Models, name)
		}); err != nil {
			writeAdminError(w, statusForConfigError(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name, "persisted": "true"})
	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) RouteGroups(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r, configPermissionForMethod(r.Method)) {
		return
	}
	name, hasName, ok := resourceName(r.URL.EscapedPath(), "/admin/route-groups")
	if !ok {
		writeAdminError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if hasName {
			group, ok := redactedConfig(h.store.Get().Config).RouteGroups[name]
			if !ok {
				writeAdminError(w, http.StatusNotFound, "route group not found")
				return
			}
			writeJSON(w, http.StatusOK, group)
			return
		}
		writeJSON(w, http.StatusOK, redactedConfig(h.store.Get().Config).RouteGroups)
	case http.MethodPut:
		if !hasName {
			writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var group config.RouteGroupConfig
		if !decodeAdminJSON(w, r, &group) {
			return
		}
		if err := h.updateConfig(func(cfg *config.Config) {
			if cfg.RouteGroups == nil {
				cfg.RouteGroups = map[string]config.RouteGroupConfig{}
			}
			cfg.RouteGroups[name] = group
		}); err != nil {
			writeAdminError(w, statusForConfigError(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "name": name, "persisted": "true"})
	case http.MethodDelete:
		if !hasName {
			writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if _, ok := h.store.Get().Config.RouteGroups[name]; !ok {
			writeAdminError(w, http.StatusNotFound, "route group not found")
			return
		}
		if err := h.updateConfig(func(cfg *config.Config) {
			delete(cfg.RouteGroups, name)
		}); err != nil {
			writeAdminError(w, statusForConfigError(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name, "persisted": "true"})
	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) ClientKeys(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r, configPermissionForMethod(r.Method)) {
		return
	}
	name, hasName, ok := resourceName(r.URL.EscapedPath(), "/admin/client-keys")
	if !ok {
		writeAdminError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		keys := redactedConfig(h.store.Get().Config).Auth.Keys
		if hasName {
			for _, key := range keys {
				if key.Name == name {
					writeJSON(w, http.StatusOK, key)
					return
				}
			}
			writeAdminError(w, http.StatusNotFound, "client key not found")
			return
		}
		writeJSON(w, http.StatusOK, keys)
	case http.MethodPut:
		if !hasName {
			writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var key config.ClientKeyConfig
		if !decodeAdminJSON(w, r, &key) {
			return
		}
		key.Name = name
		if err := h.updateConfig(func(cfg *config.Config) {
			cfg.Auth.Keys = upsertClientKey(cfg.Auth.Keys, key)
		}); err != nil {
			writeAdminError(w, statusForConfigError(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "name": name, "persisted": "true"})
	case http.MethodDelete:
		if !hasName {
			writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !clientKeyExists(h.store.Get().Config.Auth.Keys, name) {
			writeAdminError(w, http.StatusNotFound, "client key not found")
			return
		}
		if err := h.updateConfig(func(cfg *config.Config) {
			cfg.Auth.Keys = deleteClientKey(cfg.Auth.Keys, name)
		}); err != nil {
			writeAdminError(w, statusForConfigError(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name, "persisted": "true"})
	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) AccessGroups(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r, configPermissionForMethod(r.Method)) {
		return
	}
	name, hasName, ok := resourceName(r.URL.EscapedPath(), "/admin/access-groups")
	if !ok {
		writeAdminError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if hasName {
			group, ok := h.store.Get().Config.AccessGroups[name]
			if !ok {
				writeAdminError(w, http.StatusNotFound, "access group not found")
				return
			}
			writeJSON(w, http.StatusOK, group)
			return
		}
		writeJSON(w, http.StatusOK, h.store.Get().Config.AccessGroups)
	case http.MethodPut:
		if !hasName {
			writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var group config.AccessGroupConfig
		if !decodeAdminJSON(w, r, &group) {
			return
		}
		if err := h.updateConfig(func(cfg *config.Config) {
			if cfg.AccessGroups == nil {
				cfg.AccessGroups = map[string]config.AccessGroupConfig{}
			}
			cfg.AccessGroups[name] = group
		}); err != nil {
			writeAdminError(w, statusForConfigError(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "name": name, "persisted": "true"})
	case http.MethodDelete:
		if !hasName {
			writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if _, ok := h.store.Get().Config.AccessGroups[name]; !ok {
			writeAdminError(w, http.StatusNotFound, "access group not found")
			return
		}
		if err := h.updateConfig(func(cfg *config.Config) {
			delete(cfg.AccessGroups, name)
		}); err != nil {
			writeAdminError(w, statusForConfigError(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "name": name, "persisted": "true"})
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

func (h *Handler) Prometheus(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r, config.AdminPermissionMetricsRead) {
		return
	}
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(prometheusMetrics(h.recorder.Snapshot(), h.store.Get().Health())))
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

func configPermissionForMethod(method string) string {
	if method == http.MethodGet {
		return config.AdminPermissionConfigRead
	}
	return config.AdminPermissionConfigWrite
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
	if clone.Metrics.InfluxDB.Token != "" {
		clone.Metrics.InfluxDB.Token = "********"
	}
	clone.Metrics.InfluxDB.Tags = cloneStringMap(cfg.Metrics.InfluxDB.Tags)
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

func prometheusMetrics(snapshot metrics.Snapshot, health []router.EndpointHealth) string {
	var b strings.Builder
	headers := map[string]struct{}{}
	writePrometheusMetric(&b, headers, "modelrouter_requests_total", "counter", "Total proxied requests.", nil, float64(snapshot.Summary.Requests))
	writePrometheusMetric(&b, headers, "modelrouter_successes_total", "counter", "Total successful proxied requests.", nil, float64(snapshot.Summary.Successes))
	writePrometheusMetric(&b, headers, "modelrouter_failures_total", "counter", "Total failed proxied requests.", nil, float64(snapshot.Summary.Failures))
	writePrometheusMetric(&b, headers, "modelrouter_bytes_out_total", "counter", "Total response bytes written to clients.", nil, float64(snapshot.Summary.BytesOut))
	writePrometheusMetric(&b, headers, "modelrouter_prompt_tokens_total", "counter", "Total prompt tokens reported by upstreams.", nil, float64(snapshot.Summary.PromptTokens))
	writePrometheusMetric(&b, headers, "modelrouter_output_tokens_total", "counter", "Total output tokens reported by upstreams.", nil, float64(snapshot.Summary.OutputTokens))
	writePrometheusMetric(&b, headers, "modelrouter_tokens_total", "counter", "Total tokens reported by upstreams.", nil, float64(snapshot.Summary.TotalTokens))
	writePrometheusMetric(&b, headers, "modelrouter_average_latency_ms", "gauge", "Average proxied request latency in milliseconds.", nil, snapshot.Summary.AverageLatencyMS)
	writePrometheusMetric(&b, headers, "modelrouter_error_rate", "gauge", "Overall proxied request error rate.", nil, snapshot.Summary.ErrorRate)

	for _, item := range snapshot.Items {
		labels := prometheusCounterLabels(item)
		writePrometheusMetric(&b, headers, "modelrouter_route_requests_total", "counter", "Total proxied requests by client, model, route group, and endpoint.", labels, float64(item.Requests))
		writePrometheusMetric(&b, headers, "modelrouter_route_successes_total", "counter", "Successful proxied requests by client, model, route group, and endpoint.", labels, float64(item.Successes))
		writePrometheusMetric(&b, headers, "modelrouter_route_failures_total", "counter", "Failed proxied requests by client, model, route group, and endpoint.", labels, float64(item.Failures))
		writePrometheusMetric(&b, headers, "modelrouter_route_bytes_out_total", "counter", "Response bytes by client, model, route group, and endpoint.", labels, float64(item.BytesOut))
		writePrometheusMetric(&b, headers, "modelrouter_route_prompt_tokens_total", "counter", "Prompt tokens by client, model, route group, and endpoint.", labels, float64(item.PromptTokens))
		writePrometheusMetric(&b, headers, "modelrouter_route_output_tokens_total", "counter", "Output tokens by client, model, route group, and endpoint.", labels, float64(item.OutputTokens))
		writePrometheusMetric(&b, headers, "modelrouter_route_tokens_total", "counter", "Total tokens by client, model, route group, and endpoint.", labels, float64(item.TotalTokens))
		writePrometheusMetric(&b, headers, "modelrouter_route_average_latency_ms", "gauge", "Average request latency by client, model, route group, and endpoint.", labels, item.AverageLatencyMS)
		writePrometheusMetric(&b, headers, "modelrouter_route_error_rate", "gauge", "Error rate by client, model, route group, and endpoint.", labels, item.ErrorRate)

		statusCodes := make([]int, 0, len(item.StatusCodes))
		for statusCode := range item.StatusCodes {
			statusCodes = append(statusCodes, statusCode)
		}
		sort.Ints(statusCodes)
		for _, statusCode := range statusCodes {
			statusLabels := append([]prometheusLabel(nil), labels...)
			statusLabels = append(statusLabels, prometheusLabel{Name: "status_code", Value: strconv.Itoa(statusCode)})
			writePrometheusMetric(&b, headers, "modelrouter_route_status_codes_total", "counter", "Response status codes by client, model, route group, endpoint, and status code.", statusLabels, float64(item.StatusCodes[statusCode]))
		}
	}

	for _, item := range health {
		labels := []prometheusLabel{
			{Name: "route_group", Value: item.RouteGroup},
			{Name: "endpoint", Value: item.Endpoint},
			{Name: "model", Value: item.Model},
		}
		writePrometheusMetric(&b, headers, "modelrouter_endpoint_cooling", "gauge", "Whether an endpoint is currently cooling down.", labels, boolFloat(item.Cooling))
		writePrometheusMetric(&b, headers, "modelrouter_endpoint_consecutive_failures", "gauge", "Endpoint consecutive failure count.", labels, float64(item.ConsecutiveFailures))
		writePrometheusMetric(&b, headers, "modelrouter_endpoint_inflight", "gauge", "Current endpoint in-flight request count.", labels, float64(item.Inflight))
		writePrometheusMetric(&b, headers, "modelrouter_endpoint_max_concurrency", "gauge", "Configured endpoint max concurrency.", labels, float64(item.MaxConcurrency))
		writePrometheusMetric(&b, headers, "modelrouter_endpoint_last_status_code", "gauge", "Most recent upstream status code observed for an endpoint.", labels, float64(item.LastStatusCode))
	}
	return b.String()
}

type prometheusLabel struct {
	Name  string
	Value string
}

func prometheusCounterLabels(item metrics.Counter) []prometheusLabel {
	return []prometheusLabel{
		{Name: "client", Value: item.Client},
		{Name: "model", Value: item.Model},
		{Name: "route_group", Value: item.RouteGroup},
		{Name: "endpoint", Value: item.Endpoint},
	}
}

func writePrometheusMetric(b *strings.Builder, headers map[string]struct{}, name, metricType, help string, labels []prometheusLabel, value float64) {
	if _, ok := headers[name]; !ok {
		fmt.Fprintf(b, "# HELP %s %s\n", name, help)
		fmt.Fprintf(b, "# TYPE %s %s\n", name, metricType)
		headers[name] = struct{}{}
	}
	fmt.Fprintf(b, "%s%s %g\n", name, prometheusLabels(labels), value)
}

func prometheusLabels(labels []prometheusLabel) string {
	if len(labels) == 0 {
		return ""
	}
	parts := make([]string, 0, len(labels))
	for _, label := range labels {
		parts = append(parts, label.Name+`="`+prometheusEscape(label.Value)+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func prometheusEscape(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
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

func (h *Handler) updateConfig(mutator func(*config.Config)) error {
	next := cloneConfig(h.store.Get().Config)
	mutator(next)
	if err := next.Validate(); err != nil {
		return configUpdateError{status: http.StatusBadRequest, err: err}
	}
	if err := config.SaveFile(h.configPath, next); err != nil {
		return configUpdateError{status: http.StatusInternalServerError, err: err}
	}
	h.store.Update(next)
	return nil
}

type configUpdateError struct {
	status int
	err    error
}

func (e configUpdateError) Error() string {
	return e.err.Error()
}

func cloneConfig(cfg *config.Config) *config.Config {
	if cfg == nil {
		return &config.Config{}
	}
	clone := *cfg
	clone.Metrics.InfluxDB.Tags = cloneStringMap(cfg.Metrics.InfluxDB.Tags)
	clone.Admin.Keys = append([]config.AdminKeyConfig(nil), cfg.Admin.Keys...)
	for i := range clone.Admin.Keys {
		clone.Admin.Keys[i].Permissions = append([]string(nil), cfg.Admin.Keys[i].Permissions...)
	}
	clone.Auth.Keys = append([]config.ClientKeyConfig(nil), cfg.Auth.Keys...)
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
			if value.Endpoints[i].Headers != nil {
				headers := make(map[string]string, len(value.Endpoints[i].Headers))
				for header, headerValue := range value.Endpoints[i].Headers {
					headers[header] = headerValue
				}
				value.Endpoints[i].Headers = headers
			}
		}
		clone.RouteGroups[key] = value
	}
	return &clone
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func decodeAdminJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "failed to read request body")
		return false
	}
	if err := json.Unmarshal(body, out); err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

func resourceName(path, prefix string) (string, bool, bool) {
	path = strings.TrimSuffix(path, "/")
	if path == prefix {
		return "", false, true
	}
	raw := strings.TrimPrefix(path, prefix+"/")
	if raw == path || raw == "" || strings.Contains(raw, "/") {
		return "", false, false
	}
	name, err := url.PathUnescape(raw)
	if err != nil || strings.TrimSpace(name) == "" {
		return "", false, false
	}
	return name, true, true
}

func upsertClientKey(keys []config.ClientKeyConfig, key config.ClientKeyConfig) []config.ClientKeyConfig {
	out := append([]config.ClientKeyConfig(nil), keys...)
	for i := range out {
		if out[i].Name == key.Name {
			out[i] = key
			return out
		}
	}
	return append(out, key)
}

func deleteClientKey(keys []config.ClientKeyConfig, name string) []config.ClientKeyConfig {
	out := keys[:0]
	for _, key := range keys {
		if key.Name != name {
			out = append(out, key)
		}
	}
	return out
}

func clientKeyExists(keys []config.ClientKeyConfig, name string) bool {
	for _, key := range keys {
		if key.Name == name {
			return true
		}
	}
	return false
}

func statusForConfigError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if updateErr, ok := err.(configUpdateError); ok {
		return updateErr.status
	}
	return http.StatusBadRequest
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
