# Archive-at-Home Node

Archive-at-Home 分布式归档链接解析系统的工作节点。

## 功能

- 通过 WebSocket 连接到中控服务器
- 自主抢占并执行归档链接解析任务
- 使用 ExHentai Cookie 访问受限画廊
- 本地 SQLite 数据库记录解析日志
- Web Dashboard 监控节点状态

## 快速开始

### 方式一：Docker Compose（推荐）

1. 创建 `config.yaml`（参考 `config.yaml.example`）并填入配置

2. 启动服务：
```bash
docker-compose up -d
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
  cookie: "your-exhentai-cookie"  # ExHentai Cookie
  use_exhentai: true              # 是否使用 exhentai.org
  max_gp_cost: -1                 # 每日 GP 上限（-1 表示不限制）

# 任务策略配置
task:
  base_balance_gp: 1000000        # 余额基准值（高于该值不延迟抢占）
  base_claim_delay: 1             # 低余额时最大延迟（秒）

# 数据库配置
database:
  path: "./data/ehentai.db"       # SQLite 数据库路径

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
  - 登录 exhentai.org 后，从浏览器复制完整 Cookie
  - 至少需要包含 `ipb_member_id` 和 `ipb_pass_hash`
- `use_exhentai`: 是否使用 exhentai.org（推荐 true，访问受限画廊）
- `max_gp_cost`: 每日 GP 消耗上限（`-1` 表示不限制）

### 任务策略配置

- `base_balance_gp`: 余额延迟算法的基准值（高于该值时不延迟抢占）
- `base_claim_delay`: 低余额时最大抢占延迟（秒）

### 数据库配置

- `path`: SQLite 数据库路径（例如 `./data/ehentai.db`）

### Dashboard 配置

- `enabled`: 是否启用 Dashboard
- `address`: 监听地址（默认 `:8090`）

## WebSocket 协议

### 收到的消息类型

| 消息类型 | 说明 | Payload |
|---------|------|---------|
| `TASK_ANNOUNCEMENT` | 任务广播 | `{trace_id, free_tier, estimated_gp, queue_len}` |
| `TASK_ASSIGNED` | 任务分配 | `{trace_id, gallery_id, gallery_key}` |
| `TASK_GONE` | 任务已被抢占 | `{trace_id}` |

### 发送的消息类型

| 消息类型 | 说明 | Payload |
|---------|------|---------|
| `FETCH_TASK` | 抢占任务 | `{trace_id, node_id}` |
| `TASK_RESULT` | 任务结果 | `{trace_id, node_id, success, actual_gp, archive_url, error}` |

## 任务执行流程

1. **连接 Server**: 使用 ED25519 签名认证
2. **监听广播**: 收到 `TASK_ANNOUNCEMENT` 后决定是否抢占
3. **抢占任务**: 发送 `FETCH_TASK` 请求
4. **执行任务**:
   - 请求 E-Hentai API 获取归档下载链接
   - 解析 GP 消耗信息
5. **提交结果**: 发送 `TASK_RESULT` 返回结果

## 数据存储

Node 使用 SQLite 本地数据库存储解析记录：

```
node/
└── data/
    └── ehentai.db  # SQLite 数据库
```

**表结构**：
- `parse_logs`: 解析记录
  - `id`: 自增主键
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
