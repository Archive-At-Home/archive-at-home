# Archive-at-Home Server

Archive-at-Home 分布式归档链接解析系统的中控服务器。

## 功能

- HTTP API 接口（用户请求归档链接解析）
- WebSocket Hub（Node 通信）
- Redis 任务调度 + 令牌桶限流（Lua 原子脚本）
- PostgreSQL 数据持久化
- 用户认证与令牌桶限流
- 管理员后台

## 快速开始

### 方式一：Docker Compose（推荐）

1. 编辑 `docker-compose.yml`，配置环境变量（如 `ADMIN_TOKEN`、`NODE_VERIFY_KEY` 等）

2. 启动服务：
```bash
docker compose up -d
```

3. 更新服务：`main` 分支 Server 代码变更后自动构建并推送镜像，重新拉取即可：
```bash
docker compose pull server
docker compose up -d
```
如需固定版本或回滚，可将 `server/docker-compose.yml` 中的镜像 tag 改为 `sha-<commit>`（如 `server:sha-4d1ce9d`，commit 前 7 位）。

### 方式二：二进制部署

Server 不通过 Releases 分发，需自行编译：

```bash
cd server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o archive-at-home-server ./cmd/server
```

## API 文档

### 认证

所有业务接口使用 Bearer Token 认证：

```
Authorization: Bearer sk-xxxxxxxxxxxx
```

管理员接口使用独立的 Admin Token：

```
Authorization: Bearer <ADMIN_TOKEN>
```

---

## 用户 API

### POST /auth/register

邮箱注册。

**请求体:**
```json
{
  "email": "user@example.com",
  "password": "secret123",
  "nickname": "用户昵称"
}
```

**响应:**
```json
{
  "id": "abc123",
  "email": "user@example.com",
  "nickname": "用户昵称",
  "provider": "email",
  "api_key": "sk-xxxxxxxxxxxx"
}
```

### POST /auth/login

邮箱登录。

**请求体:**
```json
{
  "email": "user@example.com",
  "password": "secret123"
}
```

**响应:**
```json
{
  "id": "abc123",
  "email": "user@example.com",
  "nickname": "用户昵称",
  "provider": "email",
  "api_key": "sk-xxxxxxxxxxxx"
}
```

### GET /auth/telegram/login

Telegram 第三方登录中转页面。

**URL 参数:**
- `redirect_url` (可选): 登录成功后跳转地址
- `param_name` (可选): API Key 参数名，默认 `start`
- `botId` (可选): Telegram Bot ID，用于 Mini App 登录

**Telegram Widget 登录示例:**
```
https://your-domain.com/auth/telegram/login?redirect_url=https://t.me/YourBot
```

登录成功后跳转到：`https://t.me/YourBot?start=<api-key>`

**Telegram Mini App 登录示例:**

需要通过 Bot 的 Keyboard Button 打开此页面，`Telegram.WebApp.initData` 会自动在 Mini App 环境中可用。

页面会自动检测 `Telegram.WebApp.initData` 并完成登录。

```
https://your-domain.com/auth/telegram/login?redirect_url=https://t.me/YourBot&botId=YOUR_BOT_ID
```

登录成功后跳转到：`https://t.me/YourBot?start=<api-key>`

### POST /auth/telegram/callback

Telegram OAuth 登录回调（内部接口，由前端调用）。

---

### GET /api/v1/me 🔒

获取当前用户信息和令牌数量。

**响应:**
```json
{
  "user": {
    "id": "abc123",
    "email": "user@example.com",
    "nickname": "用户昵称",
    "provider": "email",
    "telegram_id": 1234567890,
    "api_key": "sk-xxxxxxxxxxxx",
    "status": "active",
    "level": 0,
    "last_used_at": "2026-02-11T09:12:00Z",
    "created_at": "2026-02-11T00:00:00Z"
  },
  "balance": 604800
}
```

### POST /api/v1/me/reset-key 🔒

重置 API Key（旧 Key 立即失效）。

### GET /api/v1/me/balance 🔒

获取当前令牌数量。令牌以固定速率自动补充，上限由用户等级决定。

**响应:**
```json
{
  "balance": 500000
}
```

---

### POST /api/v1/parse 🔒

**核心接口** - 请求解析画廊的归档下载链接。

**请求头:**
- `Authorization: Bearer sk-xxxxxxxxxxxx` (必需)
- `X-Client: <category>/<app>` (可选)

`X-Client` 用于标记调用来源并写入任务日志：

- **有效值**：按 `大类/应用标识` 格式直接存入
- **缺失或非法**：服务端根据 `User-Agent` 自动猜测，兜底为 `unknown/unknown`

`X-Client` 推荐格式：`大类/应用标识`，例如：
- `bot/tg-official`
- `bot/qq-xxx-xxx`
- `tampermonkey/aah-download-helper`
- `app/jhentai`

服务端规范化规则（不影响主流程）：
- 转小写并去掉首尾空白
- 仅接受 `a-z`、`0-9`、`-`、`_`、`.` 和单个 `/`
- 最大长度 64
- 不传或格式非法时自动从 `User-Agent` 识别

**请求体:**
```json
{
  "gallery_id": "3858751",
  "gallery_key": "d3de60e849",
  "force": false
}
```

**响应（成功）:**
```json
{
  "cached": false,
  "gp_cost": 180,
  "archive_url": "https://..."
}
```

**响应（从缓存）:**
```json
{
  "cached": true,
  "archive_url": "https://..."
}
```

**响应（失败 - 令牌不足）:**
```json
{
  "error": "rate limited, try again later"
}
```

---

## 管理员 API

### GET /api/v1/admin/users/:id 🔑

获取指定用户信息。

**请求头:** `Authorization: Bearer <ADMIN_TOKEN>`

**响应:**
```json
{
  "user": {
    "id": "abc123",
    "email": "user@example.com",
    "nickname": "用户昵称",
    "provider": "email",
    "telegram_id": 1234567890,
    "api_key": "sk-xxxxxxxxxxxx",
    "status": "active",
    "level": 0,
    "last_used_at": "2026-02-11T09:12:00Z",
    "created_at": "2026-02-11T00:00:00Z"
  },
  "balance": 604800
}
```

### PUT /api/v1/admin/users/:id/status 🔑

设置用户状态。

**请求头:** `Authorization: Bearer <ADMIN_TOKEN>`

**请求体:**
```json
{
  "status": "active"  // active | banned | suspended
}
```

**响应:**
```json
{
  "success": true,
  "message": "status updated to active"
}
```


### PUT /api/v1/admin/users/:id/level 🔑

设置用户等级。等级 0 为普通用户，等级 ≥1 享有更快的令牌增速和更大的令牌容量。

**请求头:** `Authorization: Bearer <ADMIN_TOKEN>`

**请求体:**
```json
{
  "level": 1
}
```

**响应:**
```json
{
  "success": true,
  "message": "level updated"
}
```


---

## WebSocket 协议

Node 通过 WebSocket 连接到 `/ws`。

### 认证

使用 `X-Auth-Token` header，格式：`NodeID:Signature`（ED25519 签名，Base64 编码）

### Server → Node

| 消息类型 | 说明 | Payload |
|---------|------|---------|
| `TASK_ASSIGNMENT` | 直接分配任务 | `{trace_id, gallery_id, gallery_key}` |

### Node → Server

| 消息类型 | 说明 | Payload |
|---------|------|---------|
| `TASK_RESULT` | 任务结果 | `{trace_id, node_id, success, retriable, actual_gp, archive_url, error}` |
| `NODE_STATUS` | 节点状态汇报（周期性） | `{have_free_quota, gp_balance, gp_cost_willingness}` |

**消息格式:**
```json
{
  "type": "TASK_ASSIGNMENT",
  "payload": { ... }
}
```

---

## 核心设计

### 直接分配调度

- Server 根据节点状态（免费额度 / GP 余额 / 消耗意愿）选择最合适的节点
- 直接发送 `TASK_ASSIGNMENT` 到指定节点，无竞速
- 失败时自动重试下一个候选节点

### 私有化缓存

- 缓存 Key: `cache:{UserID}:{GalleryID}`
- TTL: 7 天（可配置）
- 不同用户独立缓存

### 请求合并

- Key: `inflight:{UserID}:{GalleryID}`
- 同一用户短时间内重复请求自动合并

### 崩溃安全

- collapse key 设置 5 分钟 TTL
- 正常路径由 CompleteTask/FailTask 显式删除
- TTL 仅用于 server 崩溃后的自动回收


### 令牌桶限流

- 基于 Redis 哈希 + Lua 原子脚本实现令牌桶算法
- 令牌以固定速率自动补充，上限为 `maxCapacity / rate` 秒的累积量
- 默认速率 1 token/s，最大容量 604,800（7 天累积）
- 用户等级 ≥1 享有更高速率和更大容量（可配置）
- 任务派发前原子扣减令牌，失败时退还

### GP 成本追踪

- 任务先原子入队/合并（并在 Lua 内检查缓存）
- 仅在确认新建任务后请求 E-Hentai 获取预估 GP
- Node 回报实际消耗，写入任务日志

---


## Redis Lua 脚本

| 脚本 | 功能 |
|------|------|
| `LuaPublishTask` | 原子缓存检查 + 请求合并 + 创建 collapse 哨兵 |
| `LuaConsumeTokens` | 令牌桶原子 refill + 扣减，返回 OK 或 INSUFFICIENT |
| `LuaRefundTokens` | 令牌桶原子 refill + 退还，上限为 maxCapacity |

CompleteTask、FailTask、GetTokens 均为普通 Redis 命令，无需 Lua。

---

## 环境变量完整列表

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `SERVER_ADDR` | `:8080` | HTTP 监听地址 |
| `REDIS_ADDR` | `localhost:6379` | Redis 地址 |
| `REDIS_PASSWORD` | (空) | Redis 密码 |
| `REDIS_DB` | `0` | Redis DB |
| `CACHE_TTL` | `168h` | 缓存有效期 |
| `TASK_WAIT_TIMEOUT` | `90s` | HTTP 等待超时 |
| `DB_HOST` | `localhost` | PostgreSQL 主机 |
| `DB_PORT` | `5432` | PostgreSQL 端口 |
| `DB_USER` | `postgres` | PostgreSQL 用户 |
| `DB_PASSWORD` | `postgres` | PostgreSQL 密码 |
| `DB_NAME` | `postgres` | 数据库名 |
| `DB_SSLMODE` | `disable` | SSL 模式 |
| `TELEGRAM_BOT_TOKEN` | (空) | Telegram Bot Token |
| `TELEGRAM_BOT_USERNAME` | (空) | Telegram Bot 用户名 |
| `NODE_VERIFY_KEY` | (空) | ED25519 公钥 |
| `ADMIN_TOKEN` | (空) | 管理员 Token |
| `TOKEN_RATE` | `1` | 令牌增速 (tokens/s)，等级0 |
| `TOKEN_MAX_CAPACITY` | `604800` | 最大令牌容量（7天累积），等级0 |
| `TOKEN_VIP_RATE` | `5` | VIP 令牌增速，等级1+ |
| `TOKEN_VIP_CAPACITY` | `3024000` | VIP 最大令牌容量，等级1+ |
| `EMAIL_AUTH_ENABLED` | `false` | 是否启用邮箱注册/登录 |

---
