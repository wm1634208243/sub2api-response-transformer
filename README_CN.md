# Sub2API 响应状态转换代�?
这个代理放在客户�?Sub2API 之间。Sub2API 池模式把上游错误统一包成 HTTP 500 时，它可以检�?500 响应 Body，把 HTTP 状态恢复成 400�?51 等，并且默认原样返回 Body�?
请求链路�?
```text
客户 -> Nginx/端口 8888 -> 转换代理 -> �?Sub2API 1203
```

## Web 配置界面

默认管理页：

```text
http://127.0.0.1:8888/transformer/admin
```

如果你通过 Nginx 访问，就用你的域名加 `/transformer/admin`�?
页面可以配置�?
- 只处理哪�?URL：`url_path_prefixes`、`url_path_contains`
- 只处理哪些上游状态码：通常�?`500`
- �?JSON code 匹配：例�?`code`、`error.code`、`data.code`
- �?message 匹配：例�?`message`、`error.message` 包含某些关键�?- 按整�?Body 文本匹配：适合错误被包在字符串里的场景
- 转换后的 HTTP 状态码：例�?`400`、`451`
- 响应模板：留空表示原样返�?Body；填写后会替换响�?Body

建议设置 `admin_token`，保存后访问 API 配置时需要在页面顶部输入 token�?
## 响应模板

`response_template` 留空时，代理只改 HTTP 状态码，不�?Body�?
需要统一输出格式时可以填模板�?
```json
{"code":400,"message":"{{call .JSON "message"}}","data":{{call .JSON "data"}}}
```

常用变量�?
- `{{.RawBody}}`：原始响�?Body 文本
- `{{.Path}}`：请求路�?- `{{.Method}}`：请求方�?- `{{.UpstreamStatus}}`：Sub2API 返回的原状态码
- `{{.DownstreamStatus}}`：转换后的状态码
- `{{call .JSON "error.message"}}`：读�?JSON 路径

注意：模板不会自动做 JSON 转义。如�?message 里可能有引号，建议先保持模板为空，原样返回上�?Body�?
## Docker 部署

上传目录到服务器，例如：

```bash
cd /home/sub2api-response-transformer
cp config.example.json config.json
docker compose up -d --build
docker compose ps
curl -fsS http://127.0.0.1:8888/transformer/health
```

默认配置监听 `127.0.0.1:8888`，上游是 `http://127.0.0.1:1203`�?
如果直接给外网客户访问端口，需要把 `listen` 改成�?
```json
"listen": "0.0.0.0:8888"
```

更推荐通过 Nginx 暴露域名，把后端指向�?
```nginx
proxy_pass http://127.0.0.1:8888;
```

## 配置示例

```json
{
  "listen": "127.0.0.1:8888",
  "upstream": "http://127.0.0.1:1203",
  "health_path": "/transformer/health",
  "stats_path": "/transformer/stats",
  "admin_path": "/transformer/admin",
  "admin_token": "",
  "max_inspect_body_bytes": 1048576,
  "preserve_host": false,
  "emit_debug_header": false,
  "rules": [
    {
      "name": "restore-451",
      "url_path_prefixes": ["/v1/"],
      "upstream_statuses": [500],
      "json_paths": ["code", "error.code", "data.code"],
      "values": ["451"],
      "message_paths": ["message", "error.message", "data.message"],
      "message_contains": [],
      "body_contains": [],
      "case_insensitive": false,
      "downstream_status": 451,
      "response_template": ""
    }
  ]
}
```

规则从上到下匹配，第一条命中就停止。一个规则内部：

- URL 条件命中任意一个即�?- `json_paths + values` �?OR 匹配
- `message_contains` �?OR 匹配
- `body_contains` �?OR 匹配
- 如果同时配置多组条件，则这些条件组都必须命中

## 验证

```bash
curl -i http://127.0.0.1:8888/v1/chat/completions \
  -H 'Authorization: Bearer YOUR_KEY' \
  -H 'Content-Type: application/json' \
  --data '{"model":"YOUR_MODEL","messages":[{"role":"user","content":"test"}]}'
```

查看统计�?
```bash
curl -sS http://127.0.0.1:8888/transformer/stats
docker logs --tail 100 sub2api-response-transformer
```

本地测试�?
```bash
go test ./...
go vet ./...
```
