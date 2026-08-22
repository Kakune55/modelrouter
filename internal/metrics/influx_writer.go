package metrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"modelrouter/internal/config"
)

const maxInfluxErrorBodyBytes = 64 << 10

type InfluxDBWriter struct {
	client *http.Client
}

type InfluxDBWriteError struct {
	StatusCode int
	Body       string
}

func (e *InfluxDBWriteError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("InfluxDB write failed with status %d", e.StatusCode)
	}
	return fmt.Sprintf("InfluxDB write failed with status %d: %s", e.StatusCode, e.Body)
}

func (e *InfluxDBWriteError) Retryable() bool {
	return e.StatusCode == http.StatusRequestTimeout ||
		e.StatusCode == http.StatusTooManyRequests ||
		e.StatusCode >= http.StatusInternalServerError
}

func NewInfluxDBWriter(client *http.Client) *InfluxDBWriter {
	if client == nil {
		client = http.DefaultClient
	}
	return &InfluxDBWriter{client: client}
}

// WriteBatch 将一批行协议数据写入配置指定的 InfluxDB，并为整次 HTTP 请求应用超时。
func (w *InfluxDBWriter) WriteBatch(ctx context.Context, cfg config.InfluxDBConfig, lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	endpoint, authScheme, err := influxWriteEndpoint(cfg)
	if err != nil {
		return err
	}

	requestCtx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout())
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.String(), strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		return fmt.Errorf("create InfluxDB write request: %w", err)
	}
	req.Header.Set("Authorization", authScheme+" "+cfg.Token)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("Accept", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("write metrics to InfluxDB: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxInfluxErrorBodyBytes))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return fmt.Errorf("read InfluxDB write response: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close InfluxDB write response: %w", closeErr)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return &InfluxDBWriteError{
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(body)),
		}
	}
	return nil
}

func influxWriteEndpoint(cfg config.InfluxDBConfig) (*url.URL, string, error) {
	endpoint, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil {
		return nil, "", fmt.Errorf("parse InfluxDB URL: %w", err)
	}
	query := endpoint.Query()
	switch cfg.APIVersion {
	case 2:
		endpoint.Path = appendInfluxPath(endpoint.Path, "api/v2/write")
		query.Set("org", cfg.Org)
		query.Set("bucket", cfg.Bucket)
		query.Set("precision", "ns")
		endpoint.RawQuery = query.Encode()
		return endpoint, "Token", nil
	case 3:
		endpoint.Path = appendInfluxPath(endpoint.Path, "api/v3/write_lp")
		query.Set("db", cfg.Database)
		query.Set("precision", "nanosecond")
		query.Set("accept_partial", "false")
		query.Set("no_sync", "false")
		endpoint.RawQuery = query.Encode()
		return endpoint, "Bearer", nil
	default:
		return nil, "", fmt.Errorf("unsupported InfluxDB API version %d", cfg.APIVersion)
	}
}

func appendInfluxPath(basePath, endpointPath string) string {
	return "/" + path.Join(strings.Trim(basePath, "/"), endpointPath)
}
