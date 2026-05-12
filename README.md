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

### 1. 准备 .env 文件

```bash
cp .env.example .env
```

编辑 `.env`，填入必要配置：

```env
# 必填：YYDS Mail 密钥
ARCEE_SIGNUP_API_KEY=your_yydsmail_api_key

# 注册账号数量
SIGNUP_COUNT=3

# 本地 Bearer 鉴权 Key
ARCEE_OPENAI_API_KEY=daiju

# 对外端口
LISTEN_PORT=8787
```

> 不需要 `config.json`，所有配置均通过环境变量注入。

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
docker-compose run --rm -e SIGNUP_COUNT=5 arcee-signup -mode signup -count 5
docker-compose restart arcee-server
```

---

## 本地运行

### 1. 注册账号

```bash
# 通过环境变量配置
export ARCEE_SIGNUP_API_KEY=your_key

go run .                        # 注册 1 个账号
go run . -count 3               # 批量注册 3 个账号
```

注册成功后 token 写入 `tokens/` 目录。

### 2. 启动服务

```bash
export ARCEE_OPENAI_API_KEY=daiju

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

所有配置优先读取环境变量，其次读取 `config.json`（可选）。

### 环境变量

| 环境变量 | 说明 | 默认值 |
| --- | --- | --- |
| `ARCEE_SIGNUP_API_KEY` | YYDS Mail 密钥（注册模式必填） | — |
| `ARCEE_SIGNUP_DOMAIN` | 注册邮箱域名 | `xiaodai.eu.cc` |
| `ARCEE_OPENAI_API_KEY` | 本地 Bearer Key，为空则不校验 | — |
| `ARCEE_LISTEN` | 服务监听地址 | `0.0.0.0:8787` |
| `ARCEE_BASE_MODEL` | 默认模型名 | `trinity-large-thinking` |
| `ARCEE_ACCESS_TOKEN` | 手动指定单个 token（有 `tokens/` 时忽略） | — |
| `SIGNUP_COUNT` | 批量注册账号数量 | `1` |
| `LISTEN_PORT` | 宿主机对外端口 | `8787` |

### config.json（可选）

若需要使用配置文件，从模板复制：

```bash
cp config.json.ex config.json
```

环境变量优先级高于 `config.json`，Docker 部署时无需挂载配置文件。

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
