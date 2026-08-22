package metrics

import (
	"errors"
	"maps"
	"sort"
	"strconv"
	"strings"
	"time"
)

const InfluxMeasurement = "modelrouter_request"

var errInfluxTagContainsNewline = errors.New("InfluxDB tag key or value contains a newline")

var influxTagEscaper = strings.NewReplacer(
	",", "\\,",
	"=", "\\=",
	" ", "\\ ",
)

// EncodeInfluxLine 将已完成的代理请求转换为确定性的 InfluxDB 行协议数据点。
// 完成时间由调用方传入，确保重试时可以复用同一个数据点标识。
func EncodeInfluxLine(ev Event, staticTags map[string]string, completedAt time.Time) (string, error) {
	tags := make(map[string]string, len(staticTags)+8)
	maps.Copy(tags, staticTags)
	setInfluxTag(tags, "client", ev.Client)
	setInfluxTag(tags, "api_endpoint", ev.APIEndpoint)
	setInfluxTag(tags, "requested_model", ev.Model)
	setInfluxTag(tags, "model", ev.UpstreamModel)
	setInfluxTag(tags, "route_group", ev.RouteGroup)
	setInfluxTag(tags, "backend", ev.Endpoint)
	tags["status_code"] = strconv.Itoa(ev.StatusCode)
	tags["stream"] = strconv.FormatBool(ev.Streaming)

	keys := make([]string, 0, len(tags))
	for key, value := range tags {
		if containsNewline(key) || containsNewline(value) {
			return "", errInfluxTagContainsNewline
		}
		if key == "" || value == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var line strings.Builder
	line.WriteString(InfluxMeasurement)
	for _, key := range keys {
		line.WriteByte(',')
		line.WriteString(escapeInfluxTagPart(key))
		line.WriteByte('=')
		line.WriteString(escapeInfluxTagPart(tags[key]))
	}

	line.WriteString(" requests=1i")
	writeInfluxFloatField(&line, "duration_ms", durationMS(ev.Duration))
	writeInfluxFloatField(&line, "upstream_duration_ms", durationMS(ev.UpstreamDuration))
	writeInfluxIntField(&line, "bytes_out", ev.BytesOut)
	writeInfluxIntField(&line, "input_tokens", ev.PromptTokens)
	writeInfluxIntField(&line, "output_tokens", ev.OutputTokens)
	writeInfluxIntField(&line, "total_tokens", ev.TotalTokens)
	writeInfluxIntField(&line, "cache_read_tokens", ev.CacheReadTokens)
	writeInfluxIntField(&line, "reasoning_tokens", ev.ReasoningTokens)
	writeInfluxIntField(&line, "retry_count", int64(ev.RetryCount))
	writeInfluxBoolField(&line, "success", ev.Err == nil && ev.StatusCode >= 200 && ev.StatusCode < 400)
	if ev.TTFT > 0 {
		writeInfluxFloatField(&line, "ttft_ms", durationMS(ev.TTFT))
	}
	if ev.GenerationDuration > 0 {
		writeInfluxFloatField(&line, "generation_ms", durationMS(ev.GenerationDuration))
	}
	if rate := tokenRate(ev.OutputTokens, ev.Duration); rate > 0 {
		writeInfluxFloatField(&line, "end_to_end_tokens_per_second", rate)
	}
	if rate := tokenRate(ev.OutputTokens, ev.GenerationDuration); rate > 0 {
		writeInfluxFloatField(&line, "tokens_per_second", rate)
	}
	line.WriteByte(' ')
	line.WriteString(strconv.FormatInt(completedAt.UnixNano(), 10))
	return line.String(), nil
}

func setInfluxTag(tags map[string]string, key, value string) {
	if value == "" {
		delete(tags, key)
		return
	}
	tags[key] = value
}

func containsNewline(value string) bool {
	return strings.ContainsAny(value, "\r\n")
}

func escapeInfluxTagPart(value string) string {
	return influxTagEscaper.Replace(value)
}

func writeInfluxIntField(line *strings.Builder, key string, value int64) {
	line.WriteByte(',')
	line.WriteString(key)
	line.WriteByte('=')
	line.WriteString(strconv.FormatInt(value, 10))
	line.WriteByte('i')
}

func writeInfluxFloatField(line *strings.Builder, key string, value float64) {
	line.WriteByte(',')
	line.WriteString(key)
	line.WriteByte('=')
	line.WriteString(strconv.FormatFloat(value, 'f', -1, 64))
}

func writeInfluxBoolField(line *strings.Builder, key string, value bool) {
	line.WriteByte(',')
	line.WriteString(key)
	line.WriteByte('=')
	line.WriteString(strconv.FormatBool(value))
}
