package metrics

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"modelrouter/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestInfluxDBWriterWritesVersion2Batch(t *testing.T) {
	writer := NewInfluxDBWriter(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost {
			t.Fatalf("method = %s", req.Method)
		}
		if req.URL.Path != "/proxy/api/v2/write" {
			t.Fatalf("path = %s", req.URL.Path)
		}
		query := req.URL.Query()
		if query.Get("org") != "example org" || query.Get("bucket") != "router/bucket" || query.Get("precision") != "ns" {
			t.Fatalf("query = %v", query)
		}
		if got := req.Header.Get("Authorization"); got != "Token secret-v2" {
			t.Fatalf("Authorization = %q", got)
		}
		assertInfluxRequestBody(t, req, "point-a\npoint-b")
		return influxTestResponse(http.StatusNoContent, ""), nil
	})})

	err := writer.WriteBatch(context.Background(), config.InfluxDBConfig{
		APIVersion: 2,
		URL:        "https://influx.example.com/proxy/",
		Org:        "example org",
		Bucket:     "router/bucket",
		Token:      "secret-v2",
	}, []string{"point-a", "point-b"})
	if err != nil {
		t.Fatalf("WriteBatch() error = %v", err)
	}
}

func TestInfluxDBWriterWritesVersion3Batch(t *testing.T) {
	writer := NewInfluxDBWriter(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/v3/write_lp" {
			t.Fatalf("path = %s", req.URL.Path)
		}
		query := req.URL.Query()
		if query.Get("db") != "modelrouter" || query.Get("precision") != "nanosecond" {
			t.Fatalf("query = %v", query)
		}
		if query.Get("accept_partial") != "false" || query.Get("no_sync") != "false" {
			t.Fatalf("write options = %v", query)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer secret-v3" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := req.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
			t.Fatalf("Content-Type = %q", got)
		}
		if got := req.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("Accept = %q", got)
		}
		assertInfluxRequestBody(t, req, "point")
		return influxTestResponse(http.StatusNoContent, ""), nil
	})})

	err := writer.WriteBatch(context.Background(), config.InfluxDBConfig{
		APIVersion: 3,
		URL:        "https://influx.example.com",
		Database:   "modelrouter",
		Token:      "secret-v3",
	}, []string{"point"})
	if err != nil {
		t.Fatalf("WriteBatch() error = %v", err)
	}
}

func TestInfluxDBWriterReturnsStatusError(t *testing.T) {
	writer := NewInfluxDBWriter(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return influxTestResponse(http.StatusTooManyRequests, `{"error":"rate limited"}`), nil
	})})

	err := writer.WriteBatch(context.Background(), config.InfluxDBConfig{
		APIVersion: 3,
		URL:        "https://influx.example.com",
		Database:   "modelrouter",
		Token:      "secret",
	}, []string{"point"})
	var writeErr *InfluxDBWriteError
	if !errors.As(err, &writeErr) {
		t.Fatalf("WriteBatch() error = %v", err)
	}
	if writeErr.StatusCode != http.StatusTooManyRequests || writeErr.Body != `{"error":"rate limited"}` {
		t.Fatalf("write error = %+v", writeErr)
	}
	if !writeErr.Retryable() {
		t.Fatal("429 error should be retryable")
	}
}

func TestInfluxDBWriteErrorRetryable(t *testing.T) {
	for _, tt := range []struct {
		status int
		want   bool
	}{
		{status: http.StatusBadRequest, want: false},
		{status: http.StatusRequestTimeout, want: true},
		{status: http.StatusTooManyRequests, want: true},
		{status: http.StatusBadGateway, want: true},
	} {
		if got := (&InfluxDBWriteError{StatusCode: tt.status}).Retryable(); got != tt.want {
			t.Fatalf("status %d Retryable() = %t, want %t", tt.status, got, tt.want)
		}
	}
}

func TestInfluxDBWriterRespectsCallerContext(t *testing.T) {
	writer := NewInfluxDBWriter(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := writer.WriteBatch(ctx, config.InfluxDBConfig{
		APIVersion: 3,
		URL:        "https://influx.example.com",
		Database:   "modelrouter",
		Token:      "secret",
	}, []string{"point"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteBatch() error = %v", err)
	}
}

func TestInfluxDBWriterSkipsEmptyBatch(t *testing.T) {
	called := false
	writer := NewInfluxDBWriter(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		called = true
		return influxTestResponse(http.StatusNoContent, ""), nil
	})})

	if err := writer.WriteBatch(context.Background(), config.InfluxDBConfig{}, nil); err != nil {
		t.Fatalf("WriteBatch() error = %v", err)
	}
	if called {
		t.Fatal("empty batch performed an HTTP request")
	}
}

func TestInfluxDBWriterRejectsUnsupportedVersion(t *testing.T) {
	writer := NewInfluxDBWriter(nil)
	err := writer.WriteBatch(context.Background(), config.InfluxDBConfig{APIVersion: 1}, []string{"point"})
	if err == nil || !strings.Contains(err.Error(), "unsupported InfluxDB API version") {
		t.Fatalf("WriteBatch() error = %v", err)
	}
}

func assertInfluxRequestBody(t *testing.T, req *http.Request, want string) {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	if err := req.Body.Close(); err != nil {
		t.Fatalf("close request body: %v", err)
	}
	if got := string(body); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func influxTestResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
