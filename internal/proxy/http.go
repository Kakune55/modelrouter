package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type idleTimeoutContext struct {
	ctx     context.Context
	cancel  context.CancelCauseFunc
	timer   *time.Timer
	timeout time.Duration
}

func contextWithIdleTimeout(parent context.Context, timeout time.Duration) (context.Context, *idleTimeoutContext) {
	ctx, cancel := context.WithCancelCause(parent)
	idle := &idleTimeoutContext{ctx: ctx, cancel: cancel, timeout: timeout}
	if timeout > 0 {
		idle.timer = time.AfterFunc(timeout, func() {
			cancel(errUpstreamIdleTimeout)
		})
	}
	return ctx, idle
}

func (c *idleTimeoutContext) Activity() {
	if c.timer != nil {
		c.timer.Reset(c.timeout)
	}
}

func (c *idleTimeoutContext) Wrap(reader io.ReadCloser) io.ReadCloser {
	if c.timer == nil {
		return reader
	}
	return &activityReadCloser{ReadCloser: reader, activity: c.Activity}
}

func (c *idleTimeoutContext) NormalizeError(err error) error {
	if err != nil && errors.Is(context.Cause(c.ctx), errUpstreamIdleTimeout) {
		return errUpstreamIdleTimeout
	}
	return err
}

func (c *idleTimeoutContext) Close() {
	if c.timer != nil {
		c.timer.Stop()
	}
	c.cancel(nil)
}

type activityReadCloser struct {
	io.ReadCloser
	activity func()
}

func (r *activityReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.activity()
	}
	return n, err
}

func copyRequestHeaders(dst, src http.Header) {
	for key, values := range src {
		if hopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for key, values := range src {
		if hopByHopHeader(key) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func hopByHopHeader(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func isStreamResponse(header http.Header) bool {
	contentType := header.Get("Content-Type")
	return strings.Contains(contentType, "text/event-stream")
}

type streamStats struct {
	Usage              usageInfo
	TTFT               time.Duration
	GenerationDuration time.Duration
}

func copyStreaming(w http.ResponseWriter, r io.Reader, started time.Time) (int64, streamStats, error) {
	flusher, _ := w.(http.Flusher)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var total int64
	var stats streamStats
	var firstContent time.Time
	var lastContent time.Time
	var eventLines [][]byte
	observe := func(event sseEvent) {
		if event.HasUsage {
			stats.Usage = event.Usage
		}
		now := time.Now()
		if event.HasContent {
			if firstContent.IsZero() {
				firstContent = now
				stats.TTFT = firstContent.Sub(started)
			}
			lastContent = now
		}
		if event.Done && !firstContent.IsZero() {
			lastContent = now
		}
	}
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(bytes.TrimSpace(line)) == 0 {
			if len(eventLines) > 0 {
				observe(parseSSEEvent(bytes.Join(eventLines, []byte{'\n'})))
				eventLines = eventLines[:0]
			}
		} else {
			eventLines = append(eventLines, line)
		}
		written, err := w.Write(append(append([]byte(nil), line...), '\n'))
		total += int64(written)
		if flusher != nil {
			flusher.Flush()
		}
		if err != nil {
			return total, stats, err
		}
	}
	if err := scanner.Err(); err != nil {
		return total, stats, err
	}
	if len(eventLines) > 0 {
		observe(parseSSEEvent(bytes.Join(eventLines, []byte{'\n'})))
	}
	if !firstContent.IsZero() && !lastContent.IsZero() && lastContent.After(firstContent) {
		stats.GenerationDuration = lastContent.Sub(firstContent)
	}
	return total, stats, nil
}

type sseEvent struct {
	Usage      usageInfo
	HasUsage   bool
	HasContent bool
	Done       bool
}

func parseSSEEvent(line []byte) sseEvent {
	data := sseEventData(line)
	if len(data) == 0 {
		return sseEvent{}
	}
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("[DONE]")) {
		return sseEvent{Done: true}
	}
	if !bytes.Contains(data, []byte(`"usage"`)) &&
		!bytes.Contains(data, []byte(`"delta"`)) &&
		!bytes.Contains(data, []byte(`"text"`)) {
		return sseEvent{}
	}

	var payload struct {
		Usage   usageInfo `json:"usage"`
		Choices []struct {
			Delta map[string]any `json:"delta"`
			Text  string         `json:"text"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return sseEvent{}
	}
	event := sseEvent{Usage: payload.Usage}
	event.HasUsage = payload.Usage.TotalTokens > 0 || payload.Usage.PromptTokens > 0 || payload.Usage.CompletionTokens > 0
	for _, choice := range payload.Choices {
		if hasGeneratedText(choice.Delta) {
			event.HasContent = true
			break
		}
		if choice.Text != "" {
			event.HasContent = true
			break
		}
	}
	return event
}

func sseEventData(event []byte) []byte {
	lines := bytes.Split(event, []byte{'\n'})
	dataLines := make([][]byte, 0, len(lines))
	for _, line := range lines {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimPrefix(line, []byte("data:"))
		if len(data) > 0 && data[0] == ' ' {
			data = data[1:]
		}
		dataLines = append(dataLines, data)
	}
	return bytes.Join(dataLines, []byte{'\n'})
}

func hasGeneratedText(value any) bool {
	switch v := value.(type) {
	case string:
		return v != "" && v != "assistant"
	case []any:
		for _, item := range v {
			if hasGeneratedText(item) {
				return true
			}
		}
	case map[string]any:
		for key, item := range v {
			if key == "role" {
				continue
			}
			if hasGeneratedText(item) {
				return true
			}
		}
	}
	return false
}
