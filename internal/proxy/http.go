package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func contextWithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, timeout)
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
	for scanner.Scan() {
		line := scanner.Bytes()
		event := parseSSEEvent(line)
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
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return sseEvent{}
	}
	data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
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
