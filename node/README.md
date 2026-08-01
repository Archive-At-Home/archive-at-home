# Archive-at-Home Node

Archive-at-Home 分布式归档链接解析系统的工作节点。

## 功能

- 通过 WebSocket 连接到中控服务器
- 接收并执行服务器分配的任务
- 使用 ExHentai Cookie 访问受限画廊
- 本地 SQLite 数据库记录解析日志
- Web Dashboard 监控节点状态

## 快速开始

### 方式一：Docker Compose（推荐）

1. 创建 `config.yaml`（参考 `config.yaml.example`）并填入配置

2. 启动服务：
```bash
docker compose up -d
```

### 方式二：二进制部署

1. 从 [Releases](https://github.com/Archive-At-Home/archive-at-home/releases) 下载对应平台的版本

2. 解压并配置：
```bash
# Linux/macOS
tar -xzf archive-at-home-node-*.tar.gz

# Windows
unzip archive-at-home-node-windows-amd64.zip

# 创建配置文件
cp config.yaml.example config.yaml
```

3. 编辑 `config.yaml`：
```yaml
# 服务器配置
server:
  url: "ws://localhost:8080/ws"  # Server WebSocket 地址

# 节点配置（联系管理员获取）
node:
  id: "node-001"                # 节点唯一标识
  signature: "base64-signature"   # ED25519 签名

# E-Hentai 配置
ehentai:
  cookie: "your-ehentai-cookie"   # E-Hentai Cookie
  max_gp_cost: -1                 # 每日 GP 上限（-1 表示不限制）

# 任务策略配置
task:
  gp_cost_willingness: 3          # GP 消耗意愿（1-5），越高越优先获得 GP 任务

# 数据库配置
database:
  path: "./data/db.db"       # SQLite 数据库路径

# Dashboard 配置
dashboard:
  enabled: true
  address: ":8090"                # Dashboard 监听地址
```

4. 运行：
```bash
# Linux/macOS
./archive-at-home-node

# Windows
archive-at-home-node.exe
```

### 获取节点认证信息

联系平台管理员（Server 部署者）获取：
- **Node ID**: 节点唯一标识符（如 `node-001`）
- **Signature**: ED25519 签名（Base64 编码）

> **说明**: Node 使用 ED25519 签名进行身份认证。签名由 Server 管理员使用私钥生成，Node 只需要配置签名即可，无需持有私钥。

### 访问 Dashboard

打开浏览器访问 `http://localhost:8090`，可以查看：
- 节点连接状态
- 任务统计与 GP 统计
- 历史累计统计（从 SQLite 加载）
- 手动重连与手动刷新状态

## 配置说明

### Server 配置

- `url`: 中控服务器的 WebSocket 地址
- **认证**: Node 使用 ED25519 签名进行认证（联系管理员获取）

### Node 配置

- `id`: 节点唯一标识（由管理员分配）
- `signature`: Base64 编码的 ED25519 签名（由管理员生成并提供）

> **注意**: 签名由 Server 管理员使用 ED25519 私钥生成。Node 不持有私钥，只配置管理员提供的签名即可。

### E-Hentai 配置

- `cookie`: E-Hentai/ExHentai 的完整 Cookie 字符串
  - 登录 e-hentai.org 后从浏览器复制，至少包含 `ipb_member_id` 和 `ipb_pass_hash`
  - 系统自动选择可用站点，无需手动指定
  - 对于非捐赠用户，若系统无法自动获取 igneous，则需补充 `igneous=xxx`
- `max_gp_cost`: 每日 GP 消耗上限（`-1` 表示不限制）

### 任务策略配置

- `gp_cost_willingness`: GP 消耗意愿（1-5），数值越高越优先被分配 GP 任务

### 数据库配置

- `path`: SQLite 数据库路径（例如 `./data/db.db`）

### Dashboard 配置

- `enabled`: 是否启用 Dashboard
- `address`: 监听地址（默认 `:8090`）

## WebSocket 协议

### 收到的消息类型

| 消息类型 | 说明 | Payload |
|---------|------|---------|
| `TASK_ASSIGNMENT` | 服务器直接分配任务 | `{trace_id, gallery_id, gallery_key}` |

### 发送的消息类型

| 消息类型 | 说明 | Payload |
|---------|------|---------|
| `TASK_RESULT` | 任务结果 | `{trace_id, node_id, success, retriable, actual_gp, archive_url, error}` |
| `NODE_STATUS` | 节点状态汇报（周期性） | `{have_free_quota, gp_balance, gp_cost_willingness}` |

## 任务执行流程

1. **连接 Server**: 使用 ED25519 签名认证
2. **接收任务**: 收到 `TASK_ASSIGNMENT` 后直接执行
3. **执行任务**:
   - 请求 E-Hentai API 获取归档下载链接
   - 解析 GP 消耗信息
4. **提交结果**: 发送 `TASK_RESULT` 返回结果；失败时根据错误类型设置 `retriable` 标志

## 数据存储

Node 使用 SQLite 本地数据库存储解析记录：

```
node/
└── data/
    └── db.db  # SQLite 数据库
```

**表结构**：
- `parse_logs`: 解析记录
  - `id`: 文本主键（任务 `trace_id`）
  - `gid`: 画廊 ID
  - `token`: 画廊 token
  - `actual_gp`: 实际 GP 消耗
  - `estimated_size`: 估算归档大小（MiB）
  - `created_at`: 记录时间

## 故障排查

### 连接失败

- 检查 Server 是否运行
- 验证 WebSocket URL 是否正确
- 联系管理员确认节点 ID 和签名是否正确

### 认证失败

- 确认 `signature` 由管理员正确生成
- 检查签名是否正确 Base64 编码
- 联系管理员验证节点是否已授权

### Cookie 失效

- 重新登录 ExHentai，复制最新 Cookie
- 确认 Cookie 包含 `ipb_member_id` 和 `ipb_pass_hash` 等

## 部署建议

- **多节点部署**: 部署多个 Node 提高并发能力
- **分布式部署**: 不同地区/网络环境的节点可提高稳定性
- **监控**: 定期检查 Dashboard，确保节点正常运行
