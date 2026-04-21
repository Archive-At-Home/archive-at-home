# Archive-at-Home

一个基于 Go 的分布式 E-Hentai/ExHentai 归档链接解析系统，采用中控服务器 + 志愿者工作节点的架构。

## 核心特性

- **无状态广播调度** - Server 发布任务广播，Node 自主抢占，Redis Lua 原子保证互斥
- **私有化缓存** - 每个用户独立缓存，7 天 TTL
- **请求合并** - 同一用户重复请求自动合并
- **GP 成本追踪** - 自动估算和记录 GP 消耗
- **用户系统** - 支持邮箱注册 + Telegram OAuth 登录
- **余额系统** - GP 积分充值、冻结、结算、每日签到
- **管理后台** - 独立的管理员 API，支持用户管理和积分充值

## 快速开始

### 部署 Server

从 [Releases](https://github.com/Archive-At-Home/archive-at-home/releases) 下载或使用 Docker Compose：

```bash
cd server
docker-compose up -d
```

详细配置和部署方式请参考 **[server/README.md](server/README.md)**

### 部署 Node

从 [Releases](https://github.com/Archive-At-Home/archive-at-home/releases) 下载对应平台的二进制，或使用 Docker：

```bash
cd node
# 先创建 config.yaml 配置文件
docker-compose up -d
```

详细配置和部署方式请参考 **[node/README.md](node/README.md)**

### 使用 API

```bash
# 注册用户
curl -X POST http://localhost:8080/auth/register \
  -H "Content-Type: application/json" \
  -d '{"email":"user@example.com","password":"secret","nickname":"测试用户"}'

# 解析归档链接（使用返回的 api_key）
curl -X POST http://localhost:8080/api/v1/parse \
  -H "Authorization: Bearer sk-xxxxxxxxxxxx" \
  -H "Content-Type: application/json" \
  -d '{"gallery_id":"2845710","gallery_key":"a1b2c3d4e5"}'
```

完整 API 文档请参考 [server/README.md](server/README.md)

## 文档

- **[Server 文档](server/README.md)** - API 文档、配置说明、部署指南
- **[Node 文档](node/README.md)** - 配置说明、部署指南、故障排查

## 鸣谢

本项目的开发得到了以下工具的帮助：

- [Claude](https://claude.ai) - Anthropic 开发的 AI 助手，协助架构设计和代码实现
- [GitHub Copilot](https://github.com/features/copilot) - AI 代码补全工具，提升开发效率
