# modelrouter

`modelrouter` 是一个使用 Go 编写的轻量级 LLM API 统一代理服务。它对外提供 OpenAI 兼容接口，并根据请求中的模型名把流量路由到不同的上游 endpoint 组。

## 功能

- OpenAI 兼容接口：`POST /v1/chat/completions`
- OpenAI 兼容模型列表：`GET /v1/models`
- 按请求里的 `model` 字段路由
- 一个公开模型名可以绑定一个 route group
- 一个 route group 可以配置多个上游 endpoint
- 支持负载均衡策略：`round_robin`、`random`、`ip_hash`、`first_available`
- 支持 endpoint 级别的上游模型名映射
- 支持被动健康检查和失败冷却，不主动消耗 token 探测
- 支持通过 Admin API 热更新配置
- 提供内存级请求、延迟、token 和 endpoint 健康状态统计
- 支持 `text/event-stream` 流式响应透传

## 运行

```powershell
go run ./cmd/modelrouter -addr :8080 -config config.example.json
```

也可以使用自己的配置文件：

```powershell
go run ./cmd/modelrouter -addr :8080 -config config.json
```

## 配置

当前版本使用 JSON 配置。

```json
{
  "http": {
    "timeout_seconds": 120
  },
  "admin": {
    "token": "mr-replace-with-admin-token",
    "keys": [
      {
        "name": "dashboard",
        "key": "mr-replace-with-dashboard-token",
        "permissions": [
          "admin:read"
        ]
      },
      {
        "name": "config-manager",
        "key": "mr-replace-with-config-manager-token",
        "permissions": [
          "config:read",
          "config:write"
        ]
      }
    ]
  },
  "features": {
    "auto_include_stream_usage": true
  },
  "usage_log": {
    "enabled": false,
    "dir": "usage_logs",
    "retention_hours": 720
  },
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
      "allowed_models": [
        "public-*"
      ],
      "blocked_models": [
        "*-disabled"
      ],
      "rate_limit": {
        "max_concurrency": 8,
        "requests_per_minute": 120
      }
    }
  },
  "models": {
    "public-model-name": {
      "route_group": "public-model-route-group"
    }
  },
  "route_groups": {
    "public-model-route-group": {
      "strategy": "first_available",
      "passive_health": {
        "enabled": true,
        "failure_threshold": 2,
        "cooldown_seconds": 30
      },
      "endpoints": [
        {
          "name": "primary-upstream",
          "model": "provider-a/model-id",
          "base_url": "http://primary-upstream.example.com/v1",
          "api_key": "replace-with-primary-api-key",
          "max_concurrency": 8,
          "weight": 1
        },
        {
          "name": "backup-upstream",
          "model": "provider-b/model-id",
          "base_url": "http://backup-upstream.example.com/v1",
          "api_key": "replace-with-backup-api-key",
          "max_concurrency": 4,
          "weight": 1
        }
      ]
    }
  }
}
```

配置说明：

- `models`: 对外暴露的模型名。客户端请求使用这里的 key。
- `admin.token`: Admin API 的全权限 Bearer token。为空且未配置 `admin.keys` 时不校验 Admin API，生产环境不建议留空。
- `admin.keys[].name`: Admin key 名称，用于区分 WebUI、脚本或运维用户。
- `admin.keys[].key`: Admin API 使用的 Bearer token。
- `admin.keys[].permissions`: 当前 Admin key 拥有的权限列表。
- `features.auto_include_stream_usage`: 当请求为 `stream: true` 时，自动向上游注入 `stream_options.include_usage=true`，便于统计流式 token。
- `usage_log.enabled`: 是否开启用量日志落盘。默认关闭。
- `usage_log.dir`: 用量日志目录，默认 `usage_logs`。
- `usage_log.retention_hours`: 用量日志保留小时数。小于等于 `0` 时默认保留 `720` 小时。
- `auth.enabled`: 是否启用客户端 Bearer token 鉴权。
- `auth.keys[].name`: 客户端名称，用于统计维度和限流状态展示。
- `auth.keys[].key`: 客户端请求 `/v1/*` 时使用的 API token。
- `auth.keys[].access_group`: 当前 key 使用的访问权限组。
- `access_groups.<group>.allowed_models`: 当前权限组允许访问的模型模式列表。为空表示默认允许全部模型。
- `access_groups.<group>.blocked_models`: 当前权限组禁止访问的模型模式列表。黑名单优先级高于白名单。
- `access_groups.<group>.rate_limit.max_concurrency`: 使用该权限组的每个客户端最大并发请求数。小于等于 `0` 表示不限制。
- `access_groups.<group>.rate_limit.requests_per_minute`: 使用该权限组的每个客户端每分钟最大请求数。小于等于 `0` 表示不限制。
- `route_group`: 公开模型名对应的路由组。
- `route_groups`: 上游 endpoint 组。
- `strategy`: 负载均衡策略。
- `endpoints[].model`: 当前 endpoint 实际使用的上游模型 ID。
- `endpoints[].base_url`: OpenAI 兼容上游地址，例如 `http://host:port/v1`。
- `endpoints[].api_key`: 当前 endpoint 使用的上游 API key。
- `endpoints[].max_concurrency`: 当前 endpoint 最大并发数。小于等于 `0` 表示不限制。
- `passive_health.failure_threshold`: 连续失败多少次后进入冷却。
- `passive_health.cooldown_seconds`: 冷却时间，冷却期间该 endpoint 会被跳过。

模型名映射规则：

```text
endpoint.model > model.upstream_model > 请求里的公开 model 名
```

推荐把上游模型名配置在 `endpoints[].model`，因为同一个 route group 里的不同供应商可能使用不同的模型 ID。

## OpenAI 兼容接口

查看当前可用模型：

```powershell
curl.exe http://localhost:8080/v1/models `
  -H "Authorization: Bearer mr-replace-with-client-token"
```

发起 Chat Completions 请求：

```powershell
curl.exe http://localhost:8080/v1/chat/completions `
  -H "Authorization: Bearer mr-replace-with-client-token" `
  -H "Content-Type: application/json" `
  -d "{\"model\":\"public-model-name\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}"
```

代理会使用选中 endpoint 上配置的 `api_key` 请求上游。客户端侧不需要知道真实上游模型名和上游地址。

## 客户端鉴权

启用鉴权后，所有 `/v1/*` 接口都需要携带 Bearer token：

```text
Authorization: Bearer <token>
```

示例配置：

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
        "requests_per_minute": 120
      }
    }
  }
}
```

多个 key 可以复用同一个权限组：

```json
{
  "auth": {
    "enabled": true,
    "keys": [
      {
        "name": "app-a",
        "key": "mr-app-a-token",
        "access_group": "standard"
      },
      {
        "name": "app-b",
        "key": "mr-app-b-token",
        "access_group": "standard"
      }
    ]
  },
  "access_groups": {
    "standard": {
      "allowed_models": ["public-*"],
      "rate_limit": {
        "max_concurrency": 8,
        "requests_per_minute": 120
      }
    }
  }
}
```

行为：

- 未携带 token 或 token 错误：返回 `401 invalid_api_key`。
- token 不允许访问请求模型：返回 `403 model_not_allowed`。
- `allowed_models` 为空数组或不配置时，该客户端默认可访问全部模型。
- `blocked_models` 命中时会拒绝访问，即使该模型也命中了 `allowed_models`。
- 命中 client 级限流时返回 `429 rate_limit_exceeded`。
- `/v1/models` 只返回当前 token 允许访问的模型。
- `/admin/*` 使用独立的 Admin token 鉴权，不复用客户端 token。

模型访问模式支持：

- 精确匹配：`qwen3.6-27b-fp8`
- 全量通配：`*`
- 前缀匹配：`qwen-*`
- 后缀匹配：`*-fp8`
- 包含匹配：`*private*`
- 单字符匹配：`deepseek-r?`

## 路由策略

- `round_robin`: 轮询选择 endpoint。
- `random`: 随机选择 endpoint。
- `ip_hash`: 按客户端 IP 固定选择 endpoint。
- `first_available`: 优先使用第一个可用 endpoint，失败时尝试后续 endpoint。

`first_available` 适合主备模式。配合 `passive_health` 后，失败 endpoint 会被临时跳过，冷却结束后自动恢复参与路由。

非流式请求在上游连接失败、读取失败、返回 `429` 或 `5xx` 时会尝试后续 endpoint。流式请求一旦已经开始向客户端输出，就不会再切换 endpoint。

## 被动健康检查

`modelrouter` 不会主动请求上游做健康检查，因此不会额外消耗 token。

以下情况会记录一次 endpoint 失败：

- 请求上游时发生网络错误
- 请求超时
- 上游连接失败
- 上游返回 `429`
- 上游返回 `5xx`

连续失败达到 `failure_threshold` 后，endpoint 进入冷却。冷却期间路由选择会跳过该 endpoint。

## Endpoint 并发限制

可以给每个 endpoint 设置独立的最大并发：

```json
{
  "name": "primary-upstream",
  "model": "provider-a/model-id",
  "base_url": "http://primary-upstream.example.com/v1",
  "api_key": "replace-with-primary-api-key",
  "max_concurrency": 8
}
```

请求转发前会先尝试占用 endpoint 的并发名额：

- 有空余名额：请求转发给该 endpoint。
- 当前 endpoint 已满：跳过该 endpoint，尝试同一个 route group 的下一个候选 endpoint。
- 所有候选 endpoint 都已满：返回 OpenAI 风格的 `429 rate_limit_exceeded`。

这个限制只在进程内生效。多实例部署时，每个实例会分别维护自己的并发计数。

## Admin API

Admin API 支持两种鉴权方式：

- `admin.token`: 一个简单的全权限 token，适合本地或单人部署。
- `admin.keys[]`: 多个具名 token，每个 token 可以配置权限，适合 WebUI 或多人协作。

权限说明：

- `admin:*`: 全权限。
- `admin:read`: 所有只读接口，包括配置读取、指标、健康状态和限流状态。
- `admin:write`: 写接口，目前等价于配置更新和 reload。
- `config:read`: `GET /admin/config`。
- `config:write`: `PUT /admin/config`、`POST /admin/reload`。
- `metrics:read`: `/admin/metrics` 和子路径。
- `health:read`: `/admin/health`。
- `limits:read`: `/admin/limits`。

查看当前配置：

```powershell
curl.exe http://localhost:8080/admin/config `
  -H "Authorization: Bearer mr-replace-with-admin-token"
```

`GET /admin/config` 会对 `admin.token`、客户端 key 和上游 `api_key` 做脱敏。

运行时替换配置：

```powershell
curl.exe -X PUT http://localhost:8080/admin/config `
  -H "Authorization: Bearer mr-replace-with-admin-token" `
  -H "Content-Type: application/json" `
  --data-binary "@config.example.json"
```

从启动时指定的配置文件重新加载：

```powershell
curl.exe -X POST http://localhost:8080/admin/reload `
  -H "Authorization: Bearer mr-replace-with-admin-token"
```

查看运行总览：

```powershell
curl.exe http://localhost:8080/admin/overview `
  -H "Authorization: Bearer mr-replace-with-admin-token"
```

查看 endpoint 健康状态：

```powershell
curl.exe http://localhost:8080/admin/health `
  -H "Authorization: Bearer mr-replace-with-admin-token"
```

查看 client 限流状态：

```powershell
curl.exe http://localhost:8080/admin/limits `
  -H "Authorization: Bearer mr-replace-with-admin-token"
```

查看完整指标：

```powershell
curl.exe http://localhost:8080/admin/metrics `
  -H "Authorization: Bearer mr-replace-with-admin-token"
```

按条件查询明细指标：

```powershell
curl.exe "http://localhost:8080/admin/metrics?client=default-client&model=public-model-name&limit=50&offset=0" `
  -H "Authorization: Bearer mr-replace-with-admin-token"
```

查看各维度聚合：

```powershell
curl.exe http://localhost:8080/admin/metrics/clients `
  -H "Authorization: Bearer mr-replace-with-admin-token"
curl.exe http://localhost:8080/admin/metrics/models `
  -H "Authorization: Bearer mr-replace-with-admin-token"
curl.exe http://localhost:8080/admin/metrics/endpoints `
  -H "Authorization: Bearer mr-replace-with-admin-token"
```

查看最近请求事件：

```powershell
curl.exe "http://localhost:8080/admin/metrics/recent?limit=100" `
  -H "Authorization: Bearer mr-replace-with-admin-token"
```

指标接口说明：

- `/admin/overview`: 总览，包含累计 summary、最近窗口和 endpoint 健康状态。
- `/admin/health`: endpoint 健康、冷却、当前并发状态。
- `/admin/limits`: client 级限流配置、当前并发和当前分钟窗口请求数。
- `/admin/metrics`: 明细指标，包含每个 client/model/endpoint 组合的累计统计。
- `/admin/metrics/summary`: 只返回全局 summary 和最近窗口。
- `/admin/metrics/clients`: 按 client 聚合。
- `/admin/metrics/models`: 按 model 聚合。
- `/admin/metrics/endpoints`: 按 endpoint 聚合。
- `/admin/metrics/recent`: 最近请求事件，支持 `limit` 参数，最大 `1000`。

指标查询参数：

- `client`: 只看指定客户端。
- `model`: 只看指定公开模型名。
- `route_group`: 只看指定路由组。
- `endpoint`: 只看指定 endpoint。
- `limit`: 返回数量，默认 `100`，最大 `1000`。
- `offset`: 分页偏移，默认 `0`。

指标快照说明：

- 管理指标使用 1 秒内存快照缓存，避免 dashboard 高频刷新时反复全量聚合。
- 因此 `/admin/overview` 和 `/admin/metrics/*` 可能最多延迟约 1 秒反映最新请求。

支持查询参数的接口：

- `/admin/metrics`
- `/admin/metrics/clients`
- `/admin/metrics/models`
- `/admin/metrics/endpoints`
- `/admin/metrics/recent`

列表响应包含 `meta`：

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

核心指标：

- `requests`、`successes`、`failures`
- `error_rate`
- `average_latency_ms`
- `p95_latency_ms`
- `requests_per_min`
- `average_end_to_end_token_rate`
- `average_generation_token_rate`
- `average_ttft_ms`
- `bytes_per_sec`
- `prompt_tokens`、`output_tokens`、`total_tokens`
- `status_codes`

Token 速率统计：

- 非流式响应会从最终 JSON 的 `usage` 字段读取 token。
- 流式响应会从 SSE `data:` 事件里的 `usage` 字段读取 token。
- 打开 `features.auto_include_stream_usage` 后，代理会对 `stream: true` 请求自动补充 `stream_options.include_usage=true`。
- 如果上游不支持或不返回 usage，token 指标仍会是 `0`，但请求数、延迟、字节吞吐仍会正常统计。
- `average_end_to_end_token_rate` 表示端到端速率，按 `output_tokens / 总请求耗时` 计算，包含排队、prompt 处理、首 token 延迟和网络传输。
- `average_generation_token_rate` 表示流式生成速率，按 `output_tokens / (结束时间 - 首 token 时间)` 计算，更接近 CherryStudio 这类客户端展示的吐字速度。
- `average_ttft_ms` 表示平均首 token 延迟。
- 非流式响应无法准确得到首 token 时间，因此通常只有端到端速率。
- 自然时间窗口里的系统负载请看 `requests_per_min` 和 `bytes_per_sec`。

## 用量日志

用量日志默认关闭。开启后，每个完成的 `/v1/chat/completions` 请求会把一条记录投递到内存队列，由后台 goroutine 批量追加到本地 JSONL 文件：

```json
{
  "usage_log": {
    "enabled": true,
    "dir": "usage_logs",
    "retention_hours": 720
  }
}
```

日志文件按天切分，文件名类似：

```text
usage_logs/usage-2026-04-29.jsonl
```

每条记录包含 client、model、route group、endpoint、状态码、耗时、输出字节数、token 用量、TTFT、token 速率和错误摘要。不会记录请求里的 `messages`、prompt 或响应正文。

清理策略：

- `retention_hours` 控制日志保留时间。
- 小于等于 `0` 时默认保留 `720` 小时。
- 清理按文件修改时间判断，并做了节流，不会每个请求都全量扫描目录。
- 写入使用有界 channel 和批量刷盘，默认最多缓存 `4096` 条，最多攒 `100` 条或等待 `1` 秒刷盘。
- 如果磁盘长时间阻塞导致队列写满，新日志会被丢弃，以避免影响代理请求延迟。
- 服务正常退出时会尽量 flush 队列中的剩余日志。

## 注意事项

- 配置热更新会先校验新配置，校验通过后再切换。
- 正在处理中的请求会继续使用它开始时拿到的配置快照。
- `PUT /admin/config` 当前只更新内存配置，不会写回配置文件。
- 代码变更需要重启服务才会生效。
- `weight` 字段暂时保留，当前版本还没有实现加权策略。
