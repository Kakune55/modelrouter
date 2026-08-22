# modelrouter

`modelrouter` 是一个使用 Go 编写的轻量级 LLM API 统一代理服务。它对外提供 OpenAI 兼容接口，并根据请求中的模型名把流量路由到不同的上游 endpoint 组。

## 功能

- 支持 Chat Completions、Embeddings 和模型列表接口
- 按公开模型名路由，可为同一模型配置多个上游 endpoint
- 支持轮询、随机、加权、IP Hash 和主备路由
- 支持 endpoint 级模型名映射、固定请求头和请求参数覆盖
- 支持被动健康检查、失败冷却和并发限制
- 支持客户端鉴权、模型访问控制和请求限流
- 支持流式响应透传，不主动请求上游做健康探测
- 提供 Admin API、Prometheus 指标、InfluxDB 指标推送和可选的用量日志
- 支持通过 Admin API 校验并热更新配置

## 快速开始

需要 Go 1.26 或更高版本。

复制 [config.example.json](config.example.json) 为 `config.json`，至少修改以下内容：

- `route_groups.*.endpoints[].base_url`
- `route_groups.*.endpoints[].api_key`
- endpoint 或 model 对应的上游模型名
- 客户端和 Admin token

启动服务：

```powershell
go run ./cmd/modelrouter -addr :8080 -config config.json
```

发送请求：

```powershell
curl.exe http://localhost:8080/v1/chat/completions `
  -H "Authorization: Bearer mr-replace-with-client-token" `
  -H "Content-Type: application/json" `
  -d "{\"model\":\"public-model-name\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}"
```

默认监听地址是 `:8080`，默认配置文件是 `config.json`：

```powershell
go run ./cmd/modelrouter
```

## 文档

- [配置说明](docs/configuration.md)
- [接口与管理 API](docs/api.md)
- [指标与用量日志](docs/observability.md)
- [OpenAPI 文件](internal/openapidoc/openapi.json)
