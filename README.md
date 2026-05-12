# Arcee Bridge

> 自动注册 Arcee 账号，并将会话能力暴露为 OpenAI 兼容接口。支持批量注册多账号、RoundRobin 负载均衡，以及 Docker Compose 一键部署。

## 社区友链

- LINUX DO 社区: https://linux.do/

---

## 概览

这个项目做两件事：

- 用 YYDS Mail 自动创建邮箱并完成 Arcee 注册（支持批量）
- 将拿到的 `access_token` 封装成 OpenAI 风格服务，多账号 RoundRobin 轮询，方便接入现有客户端

完整链路：

`创建邮箱 -> 注册账号 -> 收取验证邮件 -> 访问验证链接 -> 登录 -> 保存 access_token -> 启动 OpenAI 兼容网关`

---

## Docker Compose 部署（推荐）

### 1. 准备配置文件

```bash
cp config.json.ex config.json   # 填入 signup.api_key
cp .env.example .env             # 设置注册账号数量
```

`config.json` 关键字段：

```json
{
  "signup": {
    "api_key": "你的YYDS邮箱密钥",
    "domain": "xiaodai.eu.cc"
  },
  "server": {
    "listen": "0.0.0.0:8787",
    "openai_api_key": "daiju"
  }
}
```

`.env` 示例：

```env
SIGNUP_COUNT=3      # 注册账号数量
LISTEN_PORT=8787    # 对外端口
```

### 2. 一键启动

```bash
docker-compose up -d
```

启动流程：
1. 自动拉取 `sunqb/arcee:latest` 镜像
2. `arcee-signup` 容器注册 `SIGNUP_COUNT` 个账号，token 写入共享 volume
3. 注册完成后 `arcee-server` 自动启动，加载所有 token，RoundRobin 轮询

### 3. 查看日志

```bash
docker-compose logs -f
```

期望输出：

```text
arcee-signup-1  | [signup 1/3] starting...
arcee-signup-1  | [signup 1/3] email=xxx@xiaodai.eu.cc
arcee-signup-1  | [signup 1/3] verified status=200
arcee-signup-1  | [signup 1/3] access_token saved to tokens/token_xxx.json
...
arcee-signup-1  | signup done: 3/3 succeeded
arcee-server-1  | loaded 3 token(s)
arcee-server-1  | openai-compatible gateway listening on http://0.0.0.0:8787
```

### 4. 追加注册更多账号

tokens 已持久化在 volume，追加注册不影响已有账号：

```bash
docker-compose run --rm arcee-signup -mode signup -count 5 -config /app/config.json
docker-compose restart arcee-server
```

---

## 本地运行

### 1. 注册账号

```bash
go run .                        # 注册 1 个账号
go run . -count 3               # 批量注册 3 个账号
```

注册成功后 token 写入 `tokens/` 目录。

### 2. 启动服务

```bash
go run . -mode serve
```

默认监听 `http://0.0.0.0:8787`，自动加载 `tokens/` 目录下所有 token。

---

## 项目结构

```text
arcee/
├── main.go               # 入口，参数解析与模式分发
├── signup.go             # 注册工作流（支持 -count N 批量）
├── server.go             # OpenAI 兼容网关，RoundRobin 多 token
├── config/
│   └── config.go         # 配置加载，LoadAllTokensFromDir
├── arcee/
│   ├── client.go
│   ├── flow.go
│   └── chat.go
├── yydsmail/
│   ├── client.go
│   ├── mailbox.go
│   ├── messages.go
│   └── inspect.go
├── Dockerfile            # 多阶段构建，distroless 基础镜像
├── docker-compose.yml    # signup + server 服务编排
├── .env.example          # 环境变量模板
├── config.json.ex        # 配置文件模板
└── tokens/               # token 存储目录（gitignore）
```

---

## 配置说明

配置文件 `config.json`（从 `config.json.ex` 复制）：

```json
{
  "mode": "signup",
  "signup": {
    "api_key": "你的YYDS邮箱密钥",
    "domain": "xiaodai.eu.cc"
  },
  "server": {
    "access_token": "",
    "listen": "0.0.0.0:8787",
    "openai_api_key": "daiju",
    "base_model_name": "trinity-large-thinking",
    "enabled_tools": ["web_search"]
  }
}
```

| 字段 | 说明 |
| --- | --- |
| `mode` | 默认运行模式，`signup` / `serve` |
| `signup.api_key` | YYDS Mail 密钥 |
| `signup.domain` | 邮箱域名 |
| `server.access_token` | 手动指定单个 token（兼容旧方式，优先读 `tokens/` 目录） |
| `server.listen` | 服务监听地址 |
| `server.openai_api_key` | 本地 Bearer Key，为空则不校验 |
| `server.base_model_name` | 默认模型名 |
| `server.enabled_tools` | 传给 Arcee 的工具开关 |

---

## OpenAI 兼容接口

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | `/healthz` | 健康检查 |
| `GET` | `/models` | 模型列表 |
| `GET` | `/v1/models` | OpenAI 风格模型列表 |
| `POST` | `/v1/chat/completions` | 聊天补全 |

```bash
# 模型列表
curl http://127.0.0.1:8787/v1/models \
  -H "Authorization: Bearer daiju"

# 聊天
curl http://127.0.0.1:8787/v1/chat/completions \
  -H "Authorization: Bearer daiju" \
  -H "Content-Type: application/json" \
  -d '{"model":"trinity-mini","messages":[{"role":"user","content":"hello"}]}'

# 流式
curl http://127.0.0.1:8787/v1/chat/completions \
  -H "Authorization: Bearer daiju" \
  -H "Content-Type: application/json" \
  -d '{"model":"trinity-large-thinking","stream":true,"messages":[{"role":"user","content":"hello"}]}'
```

---

## 模型支持

- `trinity-mini`
- `trinity-large-preview`
- `trinity-large-thinking`

---

## 安全提示

- 不要提交 `config.json` 和 `tokens/` 目录（已加入 `.gitignore`）
- `access_token` 有时效，不是永久凭证
- 监听 `0.0.0.0` 时务必配置 `server.openai_api_key`

---

## Star History

[![Star History Chart](https://starchart.cc/xn030523/arcee.svg?variant=adaptive)](https://starchart.cc/xn030523/arcee)
