# Sub2API Response Transformer

一个放在客户端与 Sub2API 之间的轻量级反向代理。当 Sub2API 将不同的上游错误统一包装为 HTTP `500` 时，本服务可以根据响应 Body 中的 JSON 字段、消息或原始文本，将状态码恢复为客户端可识别的 HTTP 状态码，例如 `400`、`429` 或 `451`。

默认情况下只修改 HTTP 状态码，**完整保留上游响应 Body**。

## 特性

- 保持普通 API、网页与静态资源请求透明透传
- 根据上游 HTTP 状态码、URL、JSON 路径、消息内容或 Body 文本匹配规则
- 支持多条规则，按从上到下顺序匹配，首条命中即停止
- 支持 `gzip` 响应检查，超出限制的 Body 保持透传
- 内置 Web 管理页，可在线修改并热加载配置
- 提供健康检查、运行统计和可选的调试响应头
- Docker Compose 部署，容器只读运行且禁止权限提升

## 工作方式

```text
Client -> Nginx or :8888 -> Response Transformer -> Sub2API :1203
```

例如，Sub2API 返回：

```http
HTTP/1.1 500 Internal Server Error
Content-Type: application/json

{"error":{"code":451,"message":"The provided prompt is considered unsafe"}}
```

使用 `restore-451` 规则后，客户端收到：

```http
HTTP/1.1 451 Unavailable For Legal Reasons
Content-Type: application/json

{"error":{"code":451,"message":"The provided prompt is considered unsafe"}}
```

## 快速开始

### 前置条件

- Linux 服务器
- Docker Engine 和 Docker Compose Plugin
- 可访问的 Sub2API 上游地址

> 当前 `docker-compose.yml` 使用 `network_mode: host`，因此仅适用于 Linux Docker 主机。默认假设 Sub2API 运行在同一服务器的 `127.0.0.1:1203`。

```bash
git clone https://github.com/wm1634208243/sub2api-response-transformer.git
cd sub2api-response-transformer
cp config.example.json config.json
```

编辑 `config.json`，至少替换 `upstream` 与 `admin_token`：

```json
{
  "listen": "127.0.0.1:8888",
  "upstream": "http://127.0.0.1:1203",
  "health_path": "/transformer/health",
  "stats_path": "/transformer/stats",
  "admin_path": "/transformer/admin",
  "admin_token": "replace-with-a-long-random-secret",
  "max_inspect_body_bytes": 1048576,
  "preserve_host": false,
  "emit_debug_header": false,
  "rules": [
    {
      "name": "restore-451",
      "url_path_prefixes": ["/v1/", "/v1beta/"],
      "url_path_contains": [],
      "upstream_statuses": [500],
      "json_paths": ["error.code"],
      "values": ["451"],
      "message_paths": [],
      "message_contains": [],
      "body_contains": [],
      "case_insensitive": false,
      "downstream_status": 451,
      "response_template": ""
    }
  ]
}
```

启动并验证：

```bash
docker compose up -d --build
docker compose ps
curl -fsS http://127.0.0.1:8888/transformer/health
docker compose logs --tail=100 response-transformer
```

预期健康检查响应：

```json
{"status":"ok"}
```

## 配置说明

| 字段 | 说明 |
| --- | --- |
| `listen` | 代理监听地址。仅经 Nginx 暴露时使用 `127.0.0.1:8888`；直接对外访问时使用 `0.0.0.0:8888`。 |
| `upstream` | Sub2API 的完整 HTTP/HTTPS 地址。host 网络下可用 `http://127.0.0.1:1203`。 |
| `health_path` | 健康检查路径，默认 `/transformer/health`。 |
| `stats_path` | JSON 统计路径，默认 `/transformer/stats`。 |
| `admin_path` | Web 管理页和配置 API 的前缀，默认 `/transformer/admin`。 |
| `admin_token` | 管理配置 API 的认证令牌。生产环境必须设置。 |
| `max_inspect_body_bytes` | 可检查响应 Body 的最大字节数，默认 `1048576`（1 MiB）。超过限制的响应不会转换，仍会透传。 |
| `preserve_host` | `false` 时将 Host 改为上游 Host；上游依赖原始 Host 时设为 `true`。 |
| `emit_debug_header` | `true` 时，对已转换响应附加 `X-Response-Transformed: <rule-name>`。生产环境通常保持 `false`。 |

保留路径由本服务处理，不会转发给 Sub2API：

```text
/transformer/admin
/transformer/health
/transformer/stats
```

## 请求参数校验

`request_validation_rules` 在请求转发到 Sub2API 前检查 JSON 标量字段。字段不存在且 `required` 为 `false` 时继续透传；字段存在但不在允许列表时，直接返回配置的状态码和 JSON Body。

```json
"request_validation_rules": [
  {
    "name": "validate-image-aspect-ratio",
    "url_path_prefixes": ["/v1beta/models"],
    "url_path_contains": [],
    "methods": ["POST"],
    "json_path": "generationConfig.imageConfig.aspectRatio",
    "allowed_values": ["1:1", "1:4", "1:8", "2:3", "3:2", "3:4", "4:1", "4:3", "4:5", "5:4", "8:1", "9:16", "16:9", "21:9"],
    "required": false,
    "case_insensitive": false,
    "downstream_status": 400,
    "response_body": "{\"error\":{\"code\":400,\"message\":\"invalid aspect ratio\",\"status\":\"INVALID_ARGUMENT\"}}"
  }
]
```

请求校验规则按配置顺序执行。只读取路径和方法命中的请求，最大检查大小复用 `max_inspect_body_bytes`；超过限制或请求不是合法 JSON 时交给 Sub2API 处理。启用 `emit_debug_header` 后，被拒绝的请求会返回 `X-Request-Rejected: <rule-name>`。


## 规则说明

规则数组为 `rules`。规则从上到下匹配，**第一条命中后立即停止**，因此具体规则应该排在通用规则前面。

| 字段 | 说明 |
| --- | --- |
| `name` | 规则名称必须非空；建议保持唯一，便于日志和调试。 |
| `upstream_statuses` | 需要检查的上游 HTTP 状态码，例如 `[500]`。 |
| `downstream_status` | 返回给客户端的 HTTP 状态码，范围为 200-599。 |
| `url_path_prefixes` | 请求路径前缀；任一前缀命中即可。 |
| `url_path_contains` | 请求路径包含的文本；任一文本命中即可。 |
| `json_paths` | 用于读取 JSON 标量值的路径，如 `error.code`、`data.code` 或 JSON Pointer `/error/code`。 |
| `values` | JSON 路径允许的值；与 `json_paths` 组成 OR 匹配。配置 `json_paths` 时必填。 |
| `message_paths` | 消息字段路径，例如 `error.message`。 |
| `message_contains` | 消息必须包含的文本；任一文本命中即可。未填 `message_paths` 时会使用常见消息路径。 |
| `body_contains` | 原始 Body 必须包含的文本；适用于 JSON 被嵌套为字符串的情况。 |
| `case_insensitive` | 是否忽略 `values`、消息和 Body 文本匹配时的大小写。 |
| `response_template` | 留空表示原样返回上游 Body；填写时使用模板输出新 Body。 |

不同条件组之间为 AND 关系：若同时配置 JSON、消息和 Body 条件，三组都必须命中。每个条件组内部为 OR 关系。

### 多条状态恢复规则

```json
"rules": [
  {
    "name": "restore-451",
    "url_path_prefixes": ["/v1/", "/v1beta/"],
    "upstream_statuses": [500],
    "json_paths": ["error.code"],
    "values": ["451"],
    "downstream_status": 451,
    "response_template": ""
  },
  {
    "name": "restore-429",
    "url_path_prefixes": ["/v1/", "/v1beta/"],
    "upstream_statuses": [500],
    "json_paths": ["error.code"],
    "values": ["429"],
    "downstream_status": 429,
    "response_template": ""
  }
]
```

### 响应模板

仅在需要改写 Body 时填写 `response_template`。可用变量包括：

- `{{.RawBody}}`：原始响应 Body 文本
- `{{.Path}}`：请求路径
- `{{.Method}}`：请求方法
- `{{.UpstreamStatus}}`：上游状态码
- `{{.DownstreamStatus}}`：转换后的状态码
- `{{call .JSON "error.message"}}`：读取 JSON 路径

模板不会自动进行 JSON 转义。若上游消息可能有引号、换行或未知结构，建议保持模板为空以安全透传原始 Body。

## Web 管理页

访问：

```text
http://SERVER_IP:8888/transformer/admin
```

管理页可编辑全局配置和多条规则。保存成功后配置立即热加载；若修改 `listen`，需要重启容器才能让监听地址生效。

当 `admin_token` 非空时，管理页加载和保存配置时必须在顶部输入同一令牌。认证可通过 `X-Admin-Token`、`Authorization: Bearer <token>` 或 `?token=<token>` 传入。

## Nginx 示例

推荐仅向公网暴露 Nginx，让容器监听回环地址：

```nginx
server {
    listen 443 ssl http2;
    server_name api.example.com;

    location / {
        proxy_pass http://127.0.0.1:8888;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

## 运行与排障

```bash
# 服务状态
docker compose ps

# 最近日志
docker compose logs --tail=100 response-transformer

# 实时日志
docker compose logs -f response-transformer

# 健康检查
curl -fsS http://127.0.0.1:8888/transformer/health

# 统计数据
curl -fsS http://127.0.0.1:8888/transformer/stats
```

统计字段：`requests`、`inspected_requests`、`rejected_requests`、`skipped_request_too_large`、`inspected_responses`、`transformed_responses`、`skipped_body_too_large`、`skipped_content_encoding` 和 `proxy_errors`。

## 安全建议

- 不要提交实际使用的 `config.json`，其中可能包含管理令牌或私有上游地址。
- 设置高熵 `admin_token`，并限制 `/transformer/admin` 的访问来源。
- 健康检查和统计端点不要求管理令牌；如有必要，请在 Nginx 或防火墙层限制访问。
- 优先使用 HTTPS 和 Nginx 暴露公网入口，不要直接暴露内部 Sub2API 端口。
- `response_template` 中不要直接拼接不可信字段为 JSON 字符串，除非已处理 JSON 转义。

## 本地开发

```bash
go test ./...
go vet ./...
go build ./...
```

Windows 本地预览程序 `transformer-preview.exe` 不参与 Linux Docker 部署，也不会纳入 Git 仓库。

## 贡献

欢迎提交 Issue 和 Pull Request。提交前请阅读 [贡献指南](CONTRIBUTING.md)，并确保测试通过。

## 许可证

本项目采用 [MIT License](LICENSE) 开源。