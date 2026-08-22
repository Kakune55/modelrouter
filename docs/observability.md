# 指标与用量日志

modelrouter 在内存中记录请求数、延迟、token、流式响应和 endpoint 状态。需要保留历史明细时，可以另外启用本地用量日志。

## 指标接口

- `/admin/overview`：累计 summary、最近窗口、endpoint 健康状态和指标导出器状态。
- `/admin/health`：endpoint 健康、冷却和当前并发状态。
- `/admin/limits`：客户端限流配置和当前状态。
- `/admin/metrics`：按 client、model、route group 和 endpoint 组合的累计指标。
- `/admin/metrics/summary`：全局 summary 和最近窗口。
- `/admin/metrics/clients`：按 client 聚合。
- `/admin/metrics/models`：按 model 聚合。
- `/admin/metrics/endpoints`：按 endpoint 聚合。
- `/admin/metrics/recent`：最近请求事件。
- `/metrics`：Prometheus text format。

这些接口使用 Admin API 鉴权。`/admin/metrics*` 和 `/metrics` 接受 `metrics:read`、`admin:read` 或 `admin:*` 权限。

查看完整指标：

```powershell
curl.exe http://localhost:8080/admin/metrics `
  -H "Authorization: Bearer mr-replace-with-admin-token"
```

获取 Prometheus 指标：

```powershell
curl.exe http://localhost:8080/metrics `
  -H "Authorization: Bearer mr-replace-with-admin-token"
```

### 查询参数

以下接口支持筛选和分页：

- `/admin/metrics`
- `/admin/metrics/clients`
- `/admin/metrics/models`
- `/admin/metrics/endpoints`
- `/admin/metrics/recent`

可用参数：

- `client`：客户端名称。
- `model`：公开模型名。
- `route_group`：路由组。
- `endpoint`：上游 endpoint。
- `limit`：返回数量，默认 `100`，最大 `1000`。
- `offset`：分页偏移，默认 `0`。

例如：

```powershell
curl.exe "http://localhost:8080/admin/metrics?client=default-client&model=public-model-name&limit=50&offset=0" `
  -H "Authorization: Bearer mr-replace-with-admin-token"
```

列表响应包含分页和筛选信息：

```json
{
  "meta": {
    "total": 120,
    "returned": 50,
    "limit": 50,
    "offset": 0,
    "filters": {
      "client": "default-client",
      "model": "public-model-name",
      "route_group": "",
      "endpoint": ""
    }
  },
  "items": []
}
```

管理指标使用 1 秒内存快照缓存，因此接口可能最多延迟约 1 秒反映最新请求。

## 指标字段

主要字段包括：

- `requests`、`successes`、`failures`、`error_rate`
- `average_latency_ms`、`p95_latency_ms`
- `requests_per_min`、`bytes_per_sec`
- `prompt_tokens`、`output_tokens`、`total_tokens`
- `average_end_to_end_token_rate`
- `average_generation_token_rate`
- `average_ttft_ms`
- `status_codes`

非流式响应从最终 JSON 的 `usage` 字段读取 token。流式响应从 SSE `data:` 事件读取，并支持一个事件包含多行 `data:`。

开启以下配置后，代理会为流式请求补充 `stream_options.include_usage=true`：

```json
{
  "features": {
    "auto_include_stream_usage": true
  }
}
```

如果上游不返回 usage，token 指标为 `0`，请求数、延迟和字节吞吐仍会正常统计。

- `average_end_to_end_token_rate` 按 `output_tokens / 总请求耗时` 计算，包含排队、prompt 处理、首 token 延迟和网络传输。
- `average_generation_token_rate` 按 `output_tokens / (结束时间 - 首 token 时间)` 计算，更接近客户端看到的吐字速度。
- `average_ttft_ms` 是平均首 token 延迟。非流式响应通常无法准确得到该值。

## InfluxDB 指标推送

启用方式和完整配置见[配置说明](configuration.md#influxdb-指标推送)。modelrouter 会为每个完成的代理请求写入一个 `modelrouter_request` 数据点，不会周期性重复推送内存累计值。

内建 tags：

- `client`
- `model`
- `route_group`
- `endpoint`
- `status_code`

主要 fields：

- `requests`，固定为整数 `1`
- `duration_ms`、`bytes_out`
- `prompt_tokens`、`output_tokens`、`total_tokens`
- `streaming`、`success`
- `ttft_ms`
- `end_to_end_token_rate`、`generation_token_rate`

不可计算的 TTFT 或 token 速率字段会省略。错误详情、prompt、messages、响应正文和任何 API token 都不会写入 InfluxDB。

推送使用有界内存队列，不在代理请求路径中等待网络写入。默认攒满 `100` 条或等待 `1` 秒后写入；`408`、`429`、`5xx` 和网络错误最多重试两次，其他 HTTP 错误直接丢弃。队列满、编码失败或最终写入失败都会计入 exporter 状态，但不会改变客户端响应。

`GET /admin/overview` 的 `metrics_exporters.influxdb` 返回：

```json
{
  "enabled": true,
  "pending_points": 0,
  "written_points": 120,
  "written_batches": 3,
  "dropped_points": 0,
  "encoding_failures": 0,
  "failed_batches": 0,
  "retries": 0,
  "last_success_unix_sec": 1787371200
}
```

正常关闭时，modelrouter 会使用独立的 `10` 秒预算刷新剩余数据点。

真实环境验证时，先通过 modelrouter 完成至少一个 Chat Completions 或 Embeddings 请求，然后检查：

- InfluxDB 3：对配置的 database 执行 `SELECT * FROM modelrouter_request ORDER BY time DESC LIMIT 10`。
- InfluxDB 2：在配置的 bucket 中按 `_measurement == "modelrouter_request"` 查询。
- `/admin/overview`：确认 `written_points` 增加且 `dropped_points`、`failed_batches` 保持为 `0`。

## 用量日志

用量日志默认关闭。开启后，每个完成的 Chat Completions 或 Embeddings 请求会写入一条 JSONL 记录：

```json
{
  "usage_log": {
    "enabled": true,
    "dir": "usage_logs",
    "retention_hours": 720
  }
}
```

- `enabled`：是否启用，默认 `false`。
- `dir`：日志目录，默认 `usage_logs`。
- `retention_hours`：保留小时数，小于等于 `0` 时默认 `720` 小时。

文件按天切分：

```text
usage_logs/usage-2026-04-29.jsonl
```

记录包含 client、model、route group、endpoint、状态码、耗时、输出字节数、token 用量、TTFT、token 速率和错误摘要。不会记录 `messages`、prompt 或响应正文。

日志通过有界内存队列批量刷盘，默认最多缓存 `4096` 条，攒满 `100` 条或等待 `1` 秒后写入。磁盘长时间阻塞导致队列满时，新记录会被丢弃，避免影响代理请求；服务正常退出时会尽量写完队列中的剩余记录。

过期文件根据修改时间清理，清理操作有节流，不会在每次请求后扫描目录。

### 查询历史用量

- `/admin/usage`：查询历史明细。
- `/admin/usage/recent`：查询最近记录。
- `/admin/usage/summary`：返回汇总、时间序列和 client/model/endpoint 排行。

明细查询支持指标接口的筛选和分页参数，还支持：

- `from`：起始 Unix 秒，默认不限制。
- `to`：结束 Unix 秒，默认不限制。

聚合查询另外支持：

- `interval`：`minute`、`hour` 或 `day`，默认 `hour`。
- `top`：排行返回数量，默认 `10`，最大 `100`。

```powershell
curl.exe "http://localhost:8080/admin/usage?client=default-client&limit=100" `
  -H "Authorization: Bearer mr-replace-with-admin-token"

curl.exe "http://localhost:8080/admin/usage/summary?interval=hour&top=10" `
  -H "Authorization: Bearer mr-replace-with-admin-token"
```
