# 接口与管理 API

## 接口索引

### 公开接口

| 方法 | 路径 | 说明 | 鉴权 |
| --- | --- | --- | --- |
| `GET` | `/v1/models` | 返回当前客户端可以访问的公开模型 | 客户端 token，启用 `auth` 时必需 |
| `POST` | `/v1/chat/completions` | Chat Completions 代理 | 客户端 token，启用 `auth` 时必需 |
| `POST` | `/v1/embeddings` | Embeddings 代理 | 客户端 token，启用 `auth` 时必需 |
| `GET` | `/openapi.json` | OpenAPI 文档 | 无 |
| `GET` | `/healthz` | 进程存活检查，正常时返回 `ok` | 无 |

### Admin 接口

`admin:*` 可以访问所有 Admin 接口。表中列出的是更小粒度的权限；`admin:read` 可以访问所有只读接口，`admin:write` 可以访问配置和客户端 Key 写入接口。为保持兼容，`config:read`、`config:write` 仍分别包含客户端 Key 的读、写权限。

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/admin/config` | `config:read` | 读取完整配置，敏感字段脱敏 |
| `PUT` | `/admin/config` | `config:write` | 校验并替换完整配置，同时写回配置文件 |
| `GET` | `/admin/models` | `config:read` | 获取模型列表 |
| `GET` | `/admin/models/{name}` | `config:read` | 获取指定模型 |
| `PUT` | `/admin/models/{name}` | `config:write` | 新增或替换指定模型 |
| `DELETE` | `/admin/models/{name}` | `config:write` | 删除指定模型 |
| `GET` | `/admin/route-groups` | `config:read` | 获取路由组列表，上游密钥脱敏 |
| `GET` | `/admin/route-groups/{name}` | `config:read` | 获取指定路由组 |
| `PUT` | `/admin/route-groups/{name}` | `config:write` | 新增或替换路由组及其 endpoints |
| `DELETE` | `/admin/route-groups/{name}` | `config:write` | 删除未被模型引用的路由组 |
| `GET` | `/admin/client-keys` | `client-keys:read` | 获取客户端 Key 列表，Key 值脱敏 |
| `POST` | `/admin/client-keys` | `client-keys:write` | 安全随机签发客户端 Key |
| `GET` | `/admin/client-keys/{name}` | `client-keys:read` | 获取指定客户端 Key，Key 值脱敏 |
| `PUT` | `/admin/client-keys/{name}` | `client-keys:write` | 新增或替换客户端 Key |
| `DELETE` | `/admin/client-keys/{name}` | `client-keys:write` | 删除客户端 Key |
| `GET` | `/admin/access-groups` | `config:read` | 获取访问组列表 |
| `GET` | `/admin/access-groups/{name}` | `config:read` | 获取指定访问组 |
| `PUT` | `/admin/access-groups/{name}` | `config:write` | 新增或替换访问组 |
| `DELETE` | `/admin/access-groups/{name}` | `config:write` | 删除未被客户端 key 引用的访问组 |
| `POST` | `/admin/reload` | `config:write` | 从启动配置文件重新加载 |
| `GET` | `/admin/overview` | `admin:read` | 获取指标、健康和限流总览 |
| `GET` | `/admin/health` | `health:read` | 获取 endpoint 健康和冷却状态 |
| `GET` | `/admin/limits` | `limits:read` | 获取客户端限流状态 |
| `GET` | `/admin/metrics` | `metrics:read` | 获取详细指标 |
| `GET` | `/admin/metrics/summary` | `metrics:read` | 获取全局汇总和最近窗口 |
| `GET` | `/admin/metrics/clients` | `metrics:read` | 按客户端聚合 |
| `GET` | `/admin/metrics/models` | `metrics:read` | 按模型聚合 |
| `GET` | `/admin/metrics/endpoints` | `metrics:read` | 按 endpoint 聚合 |
| `GET` | `/admin/metrics/recent` | `metrics:read` | 获取最近请求事件 |
| `GET` | `/admin/usage` | `metrics:read` | 查询历史用量明细 |
| `GET` | `/admin/usage/recent` | `metrics:read` | 查询最近历史用量 |
| `GET` | `/admin/usage/summary` | `metrics:read` | 查询历史用量聚合 |
| `GET` | `/metrics` | `metrics:read` | 获取 Prometheus text format 指标 |

指标接口的筛选参数和返回字段见[指标与用量日志](observability.md)。

## OpenAI 兼容接口

modelrouter 提供以下接口：

- `GET /v1/models`
- `POST /v1/chat/completions`
- `POST /v1/embeddings`

查看当前 token 可以访问的模型：

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

发起 Embeddings 请求：

```powershell
curl.exe http://localhost:8080/v1/embeddings `
  -H "Authorization: Bearer mr-replace-with-client-token" `
  -H "Content-Type: application/json" `
  -d "{\"model\":\"public-model-name\",\"input\":\"hello\"}"
```

代理会使用选中 endpoint 的上游地址、模型名和 API key。客户端不需要知道真实的上游配置。

## OpenAPI

运行中的服务通过以下地址提供 OpenAPI 文档：

```powershell
curl.exe http://localhost:8080/openapi.json
```

源文件位于 [internal/openapidoc/openapi.json](../internal/openapidoc/openapi.json)，通过 `go:embed` 打包进二进制，部署时不需要额外携带该文件。

## Admin API

Admin API 支持两种鉴权方式：

- `admin.token`：单个全权限 token，适合本地或单人部署。
- `admin.keys[]`：多个具名 token，可分别配置权限。

如果 `admin.token` 为空且没有配置 `admin.keys`，Admin API 不做鉴权。生产环境不建议这样使用。

### 权限

- `admin:*`：全权限。
- `admin:read`：所有只读接口。
- `admin:write`：配置更新、客户端 Key 写入和 reload。
- `config:read`：配置读取接口；为保持兼容，也可以读取客户端 Key。
- `config:write`：配置写入和 reload 接口；为保持兼容，也可以写入客户端 Key。
- `client-keys:read`：读取客户端 Key，Key 值始终脱敏。
- `client-keys:write`：签发、新增、替换和删除客户端 Key，不允许修改访问组或其他配置。
- `metrics:read`：`/admin/metrics*` 和 `/metrics`。
- `health:read`：`/admin/health`。
- `limits:read`：`/admin/limits`。

请求时使用对应的 Admin token：

```text
Authorization: Bearer <admin-token>
```

### 配置管理

查看当前配置：

```powershell
curl.exe http://localhost:8080/admin/config `
  -H "Authorization: Bearer mr-replace-with-admin-token"
```

响应中的 Admin token、客户端 key、上游 `api_key` 和 InfluxDB token 会被脱敏。

替换完整配置并写回配置文件：

```powershell
curl.exe -X PUT http://localhost:8080/admin/config `
  -H "Authorization: Bearer mr-replace-with-admin-token" `
  -H "Content-Type: application/json" `
  --data-binary "@config.json"
```

从启动时指定的配置文件重新加载：

```powershell
curl.exe -X POST http://localhost:8080/admin/reload `
  -H "Authorization: Bearer mr-replace-with-admin-token"
```

资源级配置接口适合 WebUI 或运维脚本使用：

- `/admin/models`、`/admin/models/{name}`
- `/admin/route-groups`、`/admin/route-groups/{name}`
- `/admin/client-keys`、`/admin/client-keys/{name}`
- `/admin/access-groups`、`/admin/access-groups/{name}`

集合和单项资源支持 `GET`，单项资源支持 `PUT` 和 `DELETE`；客户端 Key 集合额外支持 `POST` 签发。写入时会基于当前配置生成新配置，完整校验并写回配置文件，然后热更新内存配置。

资源名称包含特殊字符时需要进行 URL encode。读取 route group 和客户端 key 时，敏感字段会被脱敏。

新增或更新模型：

```powershell
curl.exe -X PUT http://localhost:8080/admin/models/public-model-name `
  -H "Authorization: Bearer mr-replace-with-admin-token" `
  -H "Content-Type: application/json" `
  -d "{\"route_group\":\"public-model-route-group\"}"
```

新增或更新客户端 key：

```powershell
curl.exe -X PUT http://localhost:8080/admin/client-keys/app-a `
  -H "Authorization: Bearer mr-replace-with-admin-token" `
  -H "Content-Type: application/json" `
  -d "{\"key\":\"mr-app-a-token\",\"access_group\":\"default-access\"}"
```

推荐通过服务端安全随机签发客户端 Key：

```powershell
curl.exe -X POST http://localhost:8080/admin/client-keys `
  -H "Authorization: Bearer mr-replace-with-key-manager-token" `
  -H "Content-Type: application/json" `
  -d "{\"name\":\"app-a\",\"access_group\":\"default-access\"}"
```

成功时返回 `201 Created`：

```json
{
  "name": "app-a",
  "key": "mr-xP7Jk0eU3wQ...",
  "access_group": "default-access"
}
```

Key 使用 `mr-` 前缀和 32 字节安全随机内容的 Base64URL 编码。明文 Key 只在签发成功响应中返回；后续查询仅返回 `********`，响应同时携带 `Cache-Control: no-store`。名称重复或客户端鉴权未启用时返回 `409`，访问组不存在时返回 `400`。`client-keys:write` 可以选择任意已经存在的访问组，但不能创建或修改访问组。

### 状态接口

- `GET /admin/overview`：累计指标、最近窗口、endpoint 健康状态和指标导出器状态。
- `GET /admin/health`：endpoint 最近状态码、错误、冷却和当前并发状态。
- `GET /admin/limits`：客户端限流配置、当前总并发、各 endpoint 当前并发和当前分钟请求数。

例如：

```powershell
curl.exe http://localhost:8080/admin/overview `
  -H "Authorization: Bearer mr-replace-with-admin-token"
```

指标和用量接口见[指标与用量日志](observability.md)。
