# 配置说明

modelrouter 使用 JSON 配置。完整示例见 [config.example.json](../config.example.json)。

启动时可以指定监听地址和配置文件：

```powershell
go run ./cmd/modelrouter -addr :8080 -config config.json
```

- `-addr`：HTTP 监听地址，默认 `:8080`。
- `-config`：配置文件路径，默认 `config.json`。

## HTTP

```json
{
  "http": {
    "idle_timeout_seconds": 120,
    "total_timeout_seconds": 0,
    "max_response_body_bytes": 67108864
  }
}
```

- `idle_timeout_seconds`：上游空闲超时。等待响应头或读取响应体时，连续没有收到数据达到该时长就取消请求；收到数据后重新计时。省略或设为 `0` 时默认 `120` 秒。
- `total_timeout_seconds`：单次上游请求的总超时，不因收到数据而重置。省略或设为 `0` 时不限制总时长。
- `timeout_seconds`：已废弃的总超时字段，仅用于兼容旧配置；`total_timeout_seconds` 大于 `0` 时以后者为准。
- `max_response_body_bytes`：非流式上游响应体最大缓冲大小。小于等于 `0` 时默认 `67108864` 字节，即 `64MB`。

## 模型与路由组

`models` 定义对外暴露的模型名，`route_groups` 定义对应的上游组：

```json
{
  "models": {
    "public-model-name": {
      "route_group": "public-model-route-group"
    }
  },
  "route_groups": {
    "public-model-route-group": {
      "strategy": "first_available",
      "endpoints": [
        {
          "name": "primary-upstream",
          "model": "provider-a/model-id",
          "base_url": "http://primary-upstream.example.com/v1",
          "api_key": "replace-with-primary-api-key"
        }
      ]
    }
  }
}
```

上游模型名按以下顺序确定：

```text
endpoint.model > model.upstream_model > 请求里的公开 model 名
```

推荐把上游模型名配置在 `endpoints[].model`。同一个 route group 接入不同供应商时，各 endpoint 往往使用不同的模型 ID。

### Endpoint 配置

- `name`：endpoint 名称，也用于指标和健康状态。
- `model`：当前 endpoint 实际使用的上游模型 ID。
- `base_url`：OpenAI 兼容上游地址，例如 `http://host:port/v1`。
- `api_key`：请求上游时使用的 API key。
- `headers`：发往上游的固定请求头，适合供应商项目 ID、组织 ID 等参数。
- `request_defaults`：补充请求 JSON 顶层字段，仅在客户端没有传入该字段时生效。
- `request_overrides`：强制覆盖请求 JSON 顶层字段。
- `max_concurrency`：endpoint 最大并发数，小于等于 `0` 表示不限制。
- `weight`：加权路由策略下的流量权重，小于等于 `0` 时按 `1` 处理。

例如：

```json
{
  "name": "primary-upstream",
  "model": "provider-a/model-id",
  "base_url": "http://primary-upstream.example.com/v1",
  "api_key": "replace-with-primary-api-key",
  "headers": {
    "X-Provider-Project": "replace-with-project-id"
  },
  "request_defaults": {
    "temperature": 0.7,
    "top_p": 0.8
  },
  "request_overrides": {
    "top_k": 20,
    "chat_template_kwargs": {
      "enable_thinking": false
    }
  },
  "max_concurrency": 8,
  "weight": 2
}
```

如果同时配置 `headers.Authorization` 和 `api_key`，最终发往上游的 `Authorization` 以 `api_key` 为准。

## 路由策略

- `round_robin`：轮询选择 endpoint。
- `random`：随机选择 endpoint。
- `weighted_round_robin`：按 `weight` 做平滑加权轮询。
- `weighted_random`：按 `weight` 做加权随机选择。
- `ip_hash`：按客户端 IP 固定选择 endpoint。
- `first_available`：优先使用第一个可用 endpoint，失败时尝试后续 endpoint。

`first_available` 适合主备模式。`weight` 只影响两个加权策略；一次请求内的 fallback 候选会去重。

非流式请求在上游连接或读取失败、返回 `429` 或 `5xx` 时，会尝试后续 endpoint。流式请求一旦开始向客户端输出，就不会切换 endpoint。

## 被动健康检查

modelrouter 不会主动请求上游做健康检查，因此不会额外消耗 token。

```json
{
  "passive_health": {
    "enabled": true,
    "failure_threshold": 2,
    "cooldown_seconds": 30
  }
}
```

网络错误、请求超时、连接失败以及上游返回 `429` 或 `5xx` 都会记为失败。连续失败达到 `failure_threshold` 后，endpoint 进入冷却；冷却期间路由会跳过它。

## 客户端鉴权与访问组

开启 `auth.enabled` 后，所有 `/v1/*` 请求都需要携带 Bearer token：

```text
Authorization: Bearer <token>
```

```json
{
  "auth": {
    "enabled": true,
    "keys": [
      {
        "name": "default-client",
        "key": "mr-replace-with-client-token",
        "access_group": "default-access"
      }
    ]
  },
  "access_groups": {
    "default-access": {
      "allowed_models": ["public-*"],
      "blocked_models": ["*-disabled"],
      "rate_limit": {
        "max_concurrency": 8,
        "max_concurrency_per_endpoint": 4,
        "requests_per_minute": 120
      }
    }
  }
}
```

- `auth.keys[].name`：客户端名称，用于指标和限流状态。
- `auth.keys[].key`：客户端 API token。
- `auth.keys[].access_group`：该 key 使用的访问组。多个 key 可以共用一个组。
- `allowed_models`：允许访问的模型模式。为空或不配置时默认允许全部模型。
- `blocked_models`：禁止访问的模型模式，优先级高于 `allowed_models`。
- `max_concurrency`：使用该访问组的每个客户端最大并发数，小于等于 `0` 表示不限制。
- `max_concurrency_per_endpoint`：每个客户端在单个 endpoint 上的最大并发数，小于等于 `0` 表示不限制。
- `requests_per_minute`：使用该访问组的每个客户端每分钟最大请求数，小于等于 `0` 表示不限制。

模型模式支持精确匹配、`*`、前后缀、包含匹配和 `?` 单字符匹配，例如：

```text
qwen3.6-27b-fp8
*
qwen-*
*-fp8
*private*
deepseek-r?
```

鉴权和限流失败使用 OpenAI 风格错误：

- token 缺失或错误：`401 invalid_api_key`
- 模型不在访问范围内：`403 model_not_allowed`
- 命中客户端限流：`429 rate_limit_exceeded`

`GET /v1/models` 只返回当前 token 可以访问的模型。Admin API 使用独立的 Admin token。

## Endpoint 并发限制

请求转发前会同时检查 endpoint 全局并发和当前客户端在该 endpoint 上的并发。任一限制已满时，会尝试同一 route group 的下一个候选；全部候选都已满时返回 `429 rate_limit_exceeded`。

`endpoints[].max_concurrency` 控制 endpoint 全局并发，`access_groups.*.rate_limit.max_concurrency_per_endpoint` 控制每个客户端在单个 endpoint 上的并发。两者互不替代，可以同时启用。配合 `first_available` 时，主 endpoint 达到当前客户端的限制后，新请求会回退到后续 endpoint。

该限制只在当前进程内生效。多实例部署时，每个实例分别维护自己的并发计数。

## InfluxDB 指标推送

InfluxDB 推送默认关闭。`api_version` 表示写入 API 版本，启用时必须明确设置为 `2` 或 `3`。
写入端点和参数分别遵循 [InfluxDB 3 write_lp API](https://docs.influxdata.com/influxdb3/core/write-data/http-api/v3-write-lp/) 与 [InfluxDB 2 write API](https://docs.influxdata.com/influxdb/v2/api/write-data/)。

InfluxDB 3 使用原生 `/api/v3/write_lp` 端点：

```json
{
  "metrics": {
    "influxdb": {
      "enabled": true,
      "api_version": 3,
      "url": "http://localhost:8181",
      "database": "modelrouter",
      "token": "replace-with-influxdb-token"
    }
  }
}
```

InfluxDB 2 使用 `/api/v2/write` 端点：

```json
{
  "metrics": {
    "influxdb": {
      "enabled": true,
      "api_version": 2,
      "url": "http://localhost:8086",
      "org": "replace-with-org-name",
      "bucket": "modelrouter",
      "token": "replace-with-influxdb-token"
    }
  }
}
```

- `enabled`：是否启用推送，默认 `false`。
- `api_version`：写入 API 版本，只接受 `2` 或 `3`；启用时必填。
- `url`：InfluxDB 基础地址，不要包含写入端点；支持反向代理路径前缀。
- `org`、`bucket`：InfluxDB 2 必填。
- `database`：InfluxDB 3 必填。
- `token`：写入 token，启用时必填；通过 Admin API 读取配置时会被脱敏。
- `tags`：附加到每个数据点的静态 tag，例如 `instance`、`environment`。内建的 `client`、`api_endpoint`、`requested_model`、`model`、`route_group`、`backend`、`status_code`、`stream` 优先。
- `batch_size`：每批最大数据点数，省略或设为 `0` 时默认 `100`。
- `flush_interval_seconds`：未达到批量大小时的刷新间隔，省略或设为 `0` 时默认 `1` 秒。
- `queue_size`：进程内最多待处理数据点数，省略或设为 `0` 时默认 `4096`。
- `timeout_seconds`：单次 InfluxDB HTTP 写入超时，省略或设为 `0` 时默认 `5` 秒。

普通部署只需要填写连接字段。批量、队列和超时参数可以全部省略。写入行为、数据字段和运行状态见[指标与用量日志](observability.md#influxdb-指标推送)。

## 其他配置

- `features.auto_include_stream_usage`：当请求为 `stream: true` 时，自动向上游补充 `stream_options.include_usage=true`。
- `usage_log`：本地用量日志配置，见[指标与用量日志](observability.md#用量日志)。
- `admin`：Admin API token 和权限，见[接口与管理 API](api.md#admin-api)。

## 热更新行为

- 新配置通过完整校验后才会切换。
- 正在处理的请求继续使用开始时取得的配置快照。
- `PUT /admin/config` 先写回启动时指定的配置文件，再切换内存配置。
- InfluxDB 配置更新后，新数据点使用新配置；已经入队的数据点仍写入原目标，不会混批。
- 代码变更仍需重启服务。
