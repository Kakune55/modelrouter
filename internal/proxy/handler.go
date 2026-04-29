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
	"modelrouter/internal/usage"
)

var errEndpointConcurrencyLimited = errors.New("all candidate endpoints are at max concurrency")

type Handler struct {
	store         *router.Store
	recorder      *metrics.Recorder
	usageLogger   *usage.Logger
	client        *http.Client
	clientLimiter *clientLimiter
}

func NewHandler(store *router.Store, recorder *metrics.Recorder) *Handler {
	return &Handler{
		store:         store,
		recorder:      recorder,
		usageLogger:   usage.NewLogger(),
		clientLimiter: newClientLimiter(),
		client: &http.Client{
			Timeout: 0,
		},
	}
}

func (h *Handler) Close() {
	if h == nil || h.usageLogger == nil {
		return
	}
	h.usageLogger.Close()
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
		if errors.Is(err, router.ErrNoAvailableEndpoint) {
			writeOpenAIError(w, http.StatusServiceUnavailable, err.Error(), "upstream_unavailable")
			return
		}
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "model_not_found")
		return
	}

	start := time.Now()
	status, usage, bytesOut, endpoint, responseStats, err := h.forwardWithFallback(w, r, body, snap.Config.Timeout(), snap.Config.Features, route)
	duration := time.Since(start)
	if endpoint.Name == "" {
		endpoint = route.Endpoint
	}
	event := metrics.Event{
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
	}
	h.recorder.Record(event)
	_ = h.usageLogger.Record(snap.Config.UsageLog, event)

	if err != nil {
		if responseStats.ResponseStarted {
			return
		}
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
	ResponseStarted    bool
}

type upstreamResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	Usage      usageInfo
	BytesOut   int64
	Stats      responseStats
}

func (h *Handler) forwardWithFallback(w http.ResponseWriter, r *http.Request, body []byte, timeout time.Duration, features config.FeaturesConfig, route *router.Route) (int, usageInfo, int64, config.EndpointConfig, responseStats, error) {
	endpoints := route.Candidates()
	var lastErr error
	var lastResponse *upstreamResponse
	var lastEndpoint config.EndpointConfig
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
		resp, err := h.forward(w, r, upstreamBody, timeout, endpoint)
		route.Release(endpoint.Name)
		if err != nil {
			route.MarkFailure(endpoint.Name)
			if resp != nil && resp.Stats.ResponseStarted {
				return resp.StatusCode, resp.Usage, resp.BytesOut, endpoint, resp.Stats, err
			}
			lastErr = err
			continue
		}
		if endpointFailureStatus(resp.StatusCode) {
			route.MarkFailure(endpoint.Name)
			if resp.Stats.ResponseStarted {
				return resp.StatusCode, resp.Usage, resp.BytesOut, endpoint, resp.Stats, nil
			}
			lastResponse = resp
			lastEndpoint = endpoint
			continue
		} else {
			route.MarkSuccess(endpoint.Name)
		}
		if !resp.Stats.ResponseStarted {
			bytesOut, err := writeBufferedResponse(w, resp)
			resp.BytesOut = bytesOut
			if err != nil {
				return resp.StatusCode, resp.Usage, resp.BytesOut, endpoint, resp.Stats, err
			}
		}
		return resp.StatusCode, resp.Usage, resp.BytesOut, endpoint, resp.Stats, nil
	}
	if lastResponse != nil {
		bytesOut, err := writeBufferedResponse(w, lastResponse)
		lastResponse.BytesOut = bytesOut
		return lastResponse.StatusCode, lastResponse.Usage, lastResponse.BytesOut, lastEndpoint, lastResponse.Stats, err
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

func (h *Handler) forward(w http.ResponseWriter, r *http.Request, body []byte, timeout time.Duration, endpoint config.EndpointConfig) (*upstreamResponse, error) {
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
		return nil, err
	}
	copyRequestHeaders(req.Header, r.Header)
	if endpoint.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+endpoint.APIKey)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if isStreamResponse(resp.Header) {
		copyResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		n, stats, err := copyStreaming(w, resp.Body, started)
		return &upstreamResponse{
			StatusCode: resp.StatusCode,
			Usage:      stats.Usage,
			BytesOut:   n,
			Stats: responseStats{
				Streaming:          true,
				TTFT:               stats.TTFT,
				GenerationDuration: stats.GenerationDuration,
				ResponseStarted:    true,
			},
		}, err
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return &upstreamResponse{StatusCode: resp.StatusCode, Header: resp.Header.Clone()}, err
	}
	return &upstreamResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       respBody,
		Usage:      parseUsage(respBody),
		Stats:      responseStats{},
	}, nil
}

func writeBufferedResponse(w http.ResponseWriter, resp *upstreamResponse) (int64, error) {
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	n, err := w.Write(resp.Body)
	return int64(n), err
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
