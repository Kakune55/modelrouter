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
  "auth": {
    "enabled": true,
    "keys": [
      {
        "name": "default-client",
        "key": "mr-replace-with-client-token"
      }
    ]
  },
  "access": {
    "default-client": {
      "allowed_models": [
        "public-*"
      ],
      "blocked_models": [
        "*-disabled"
      ]
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
- `auth.enabled`: 是否启用客户端 Bearer token 鉴权。
- `auth.keys[].name`: 客户端名称，用来关联 `access` 权限和统计维度。
- `auth.keys[].key`: 客户端请求 `/v1/*` 时使用的 API token。
- `access.<client>.allowed_models`: 当前客户端允许访问的模型模式列表。为空表示默认允许全部模型。
- `access.<client>.blocked_models`: 当前客户端禁止访问的模型模式列表。黑名单优先级高于白名单。
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
        "key": "mr-replace-with-client-token"
      }
    ]
  },
  "access": {
    "default-client": {
      "allowed_models": ["public-*"],
      "blocked_models": ["*-disabled"]
    }
  }
}
```

行为：

- 未携带 token 或 token 错误：返回 `401 invalid_api_key`。
- token 不允许访问请求模型：返回 `403 model_not_allowed`。
- `allowed_models` 为空数组或不配置时，该客户端默认可访问全部模型。
- `blocked_models` 命中时会拒绝访问，即使该模型也命中了 `allowed_models`。
- `/v1/models` 只返回当前 token 允许访问的模型。
- `/admin/*` 当前不走客户端 token 鉴权，建议只暴露在可信网络内。

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

查看当前配置：

```powershell
curl.exe http://localhost:8080/admin/config
```

运行时替换配置：

```powershell
curl.exe -X PUT http://localhost:8080/admin/config `
  -H "Content-Type: application/json" `
  --data-binary "@config.example.json"
```

从启动时指定的配置文件重新加载：

```powershell
curl.exe -X POST http://localhost:8080/admin/reload
```

查看统计和 endpoint 健康状态：

```powershell
curl.exe http://localhost:8080/admin/stats
```

## 注意事项

- 配置热更新会先校验新配置，校验通过后再切换。
- 正在处理中的请求会继续使用它开始时拿到的配置快照。
- `PUT /admin/config` 当前只更新内存配置，不会写回配置文件。
- 代码变更需要重启服务才会生效。
- `weight` 字段暂时保留，当前版本还没有实现加权策略。
