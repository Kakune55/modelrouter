package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"modelrouter/internal/config"
	"modelrouter/internal/metrics"
	"modelrouter/internal/router"
)

var errEndpointConcurrencyLimited = errors.New("all candidate endpoints are at max concurrency")

type Handler struct {
	store         *router.Store
	recorder      *metrics.Recorder
	client        *http.Client
	clientLimiter *clientLimiter
}

func NewHandler(store *router.Store, recorder *metrics.Recorder) *Handler {
	return &Handler{
		store:         store,
		recorder:      recorder,
		clientLimiter: newClientLimiter(),
		client: &http.Client{
			Timeout: 0,
		},
	}
}

func (h *Handler) ClientLimitStatus() any {
	return h.clientLimiter.status(h.store.Get().Config, time.Now())
}

func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed", "method_not_allowed")
		return
	}
	client, ok := h.authenticate(r)
	if !ok {
		writeUnauthorized(w, "invalid or missing API key")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "failed to read request body", "bad_request")
		return
	}

	modelName, err := readModel(body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "bad_request")
		return
	}
	if !client.CanAccessModel(modelName) {
		writeOpenAIError(w, http.StatusForbidden, "model is not allowed for this API key", "model_not_allowed")
		return
	}
	limit := h.store.Get().Config.AccessGroups[client.AccessGroup].RateLimit
	decision := h.clientLimiter.acquire(client.Name, limit, time.Now())
	if !decision.Allowed {
		writeOpenAIError(w, http.StatusTooManyRequests, decision.Reason, "rate_limit_exceeded")
		return
	}
	defer h.clientLimiter.release(client.Name)

	snap := h.store.Get()
	route, err := snap.Pick(modelName, clientIP(r))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "model_not_found")
		return
	}

	start := time.Now()
	status, usage, bytesOut, endpoint, responseStats, err := h.forwardWithFallback(w, r, body, snap.Config.Timeout(), snap.Config.Features, route)
	duration := time.Since(start)
	if endpoint.Name == "" {
		endpoint = route.Endpoint
	}
	h.recorder.Record(metrics.Event{
		Client:             client.Name,
		Model:              modelName,
		RouteGroup:         route.Group,
		Endpoint:           endpoint.Name,
		StatusCode:         status,
		Duration:           duration,
		BytesOut:           bytesOut,
		PromptTokens:       usage.PromptTokens,
		OutputTokens:       usage.CompletionTokens,
		TotalTokens:        usage.TotalTokens,
		Streaming:          responseStats.Streaming,
		TTFT:               responseStats.TTFT,
		GenerationDuration: responseStats.GenerationDuration,
		Err:                err,
	})

	if err != nil {
		if errors.Is(err, errEndpointConcurrencyLimited) {
			writeOpenAIError(w, http.StatusTooManyRequests, err.Error(), "rate_limit_exceeded")
			return
		}
		writeOpenAIError(w, http.StatusBadGateway, err.Error(), "upstream_error")
	}
}

type responseStats struct {
	Streaming          bool
	TTFT               time.Duration
	GenerationDuration time.Duration
}

func (h *Handler) forwardWithFallback(w http.ResponseWriter, r *http.Request, body []byte, timeout time.Duration, features config.FeaturesConfig, route *router.Route) (int, usageInfo, int64, config.EndpointConfig, responseStats, error) {
	endpoints := route.Candidates()
	var lastErr error
	limited := false
	for _, endpoint := range endpoints {
		if !route.TryAcquire(endpoint.Name) {
			limited = true
			continue
		}
		upstreamBody, err := prepareUpstreamBody(body, upstreamModel(route, endpoint), features)
		if err != nil {
			route.Release(endpoint.Name)
			return http.StatusBadRequest, usageInfo{}, 0, endpoint, responseStats{}, err
		}
		status, usage, bytesOut, stats, err := h.forward(w, r, upstreamBody, timeout, endpoint)
		route.Release(endpoint.Name)
		if err != nil {
			route.MarkFailure(endpoint.Name)
			lastErr = err
			continue
		}
		if endpointFailureStatus(status) {
			route.MarkFailure(endpoint.Name)
		} else {
			route.MarkSuccess(endpoint.Name)
		}
		return status, usage, bytesOut, endpoint, stats, nil
	}
	if lastErr == nil {
		if limited {
			return http.StatusTooManyRequests, usageInfo{}, 0, config.EndpointConfig{}, responseStats{}, errEndpointConcurrencyLimited
		}
		lastErr = errors.New("no upstream endpoint available")
	}
	return http.StatusBadGateway, usageInfo{}, 0, config.EndpointConfig{}, responseStats{}, lastErr
}

func endpointFailureStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func upstreamModel(route *router.Route, endpoint config.EndpointConfig) string {
	if strings.TrimSpace(endpoint.Model) != "" {
		return endpoint.Model
	}
	if strings.TrimSpace(route.UpstreamModel) != "" {
		return route.UpstreamModel
	}
	return route.Model
}

func (h *Handler) forward(w http.ResponseWriter, r *http.Request, body []byte, timeout time.Duration, endpoint config.EndpointConfig) (int, usageInfo, int64, responseStats, error) {
	started := time.Now()
	ctx := r.Context()
	if timeout > 0 {
		var cancel func()
		ctx, cancel = contextWithTimeout(ctx, timeout)
		defer cancel()
	}

	upstreamURL := strings.TrimRight(endpoint.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return http.StatusBadGateway, usageInfo{}, 0, responseStats{}, err
	}
	copyRequestHeaders(req.Header, r.Header)
	if endpoint.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+endpoint.APIKey)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return http.StatusBadGateway, usageInfo{}, 0, responseStats{}, err
	}
	defer resp.Body.Close()

	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	if isStreamResponse(resp.Header) {
		n, stats, err := copyStreaming(w, resp.Body, started)
		return resp.StatusCode, stats.Usage, n, responseStats{Streaming: true, TTFT: stats.TTFT, GenerationDuration: stats.GenerationDuration}, err
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, usageInfo{}, 0, responseStats{}, err
	}
	n, writeErr := w.Write(respBody)
	if writeErr != nil {
		return resp.StatusCode, usageInfo{}, int64(n), responseStats{}, writeErr
	}
	return resp.StatusCode, parseUsage(respBody), int64(n), responseStats{}, nil
}

func readModel(body []byte) (string, error) {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return "", errors.New("request body must be valid JSON")
	}
	if strings.TrimSpace(req.Model) == "" {
		return "", errors.New("model is required")
	}
	return req.Model, nil
}

func prepareUpstreamBody(body []byte, upstreamModel string, features config.FeaturesConfig) ([]byte, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, errors.New("request body must be valid JSON")
	}
	if strings.TrimSpace(upstreamModel) != "" {
		req["model"] = upstreamModel
	}
	if features.AutoIncludeStreamUsage && requestIsStreaming(req) {
		streamOptions, _ := req["stream_options"].(map[string]any)
		if streamOptions == nil {
			streamOptions = map[string]any{}
		}
		streamOptions["include_usage"] = true
		req["stream_options"] = streamOptions
	}
	rewritten, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	return rewritten, nil
}

func requestIsStreaming(req map[string]any) bool {
	stream, ok := req["stream"].(bool)
	return ok && stream
}

func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		return strings.TrimSpace(parts[0])
	}
	if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
		return strings.TrimSpace(realIP)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
