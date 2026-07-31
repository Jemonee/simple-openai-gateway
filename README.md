# OpenAI 网关控制台

一个使用 Go、Gin、SQLite 和 Vue 3 构建的 OpenAI 兼容多渠道网关。它在单个管理控制台中提供上游渠道、模型映射、访问令牌、调用日志和会话时间线管理，适合本地部署、内部服务接入和网关能力验证。

仓库地址：[Jemonee/simple-openai-gateway](https://github.com/Jemonee/simple-openai-gateway)

当前功能分支：`codex/feature-openai-gateway`

## 主要功能

- 兼容 `GET /v1/models`、`POST /v1/chat/completions` 和 `POST /v1/responses`
- 管理多个 OpenAI 兼容上游渠道及渠道模型映射
- 按公开模型配置路由优先级、会话亲和、失败重试和熔断切换
- 签发、轮换、停用客户端访问令牌，统计 Token 与费用
- 查看运行总览、调用日志和最近五天的会话渠道时间线
- 在时间线中展示渠道切换前后快照及停用、熔断、限流、5xx、网络或响应错误等原因
- 管理员登录、密码修改、Cookie 安全属性及请求超时配置
- 单个 Go 二进制嵌入前端资源，使用 SQLite 保存配置和运行数据

## 快速开始

环境要求：Go `1.25.4`、Node.js `^20.19.0 || >=22.12.0`、pnpm `10.23.0`。

```bash
go mod download
pnpm --dir frontend install --frozen-lockfile
pnpm --dir frontend build
cp .env.example .env
go run .
```

服务默认监听 `0.0.0.0:8888`，管理控制台地址为 `http://127.0.0.1:8888/static/`。如不需要启动时自动打开浏览器，在 `.env` 中设置 `GATEWAY_OPEN_BROWSER=false`。首次启动时若关键环境变量缺失，程序会在当前目录创建或补全权限为 `0600` 的 `.env`，生成随机主密钥和管理员登录凭据；请在登录前查看并妥善保管这些值。

也可以在首次启动前自行配置以下环境变量；未配置的必需项会自动生成：

| 环境变量 | 要求 | 用途 |
| --- | --- | --- |
| `GATEWAY_MASTER_KEY` | 必需；Base64 编码的 32 字节密钥 | AES-GCM 加密上游渠道 API Key；已有渠道后不得更换 |
| `GATEWAY_ADMIN_USERNAME` | 数据库尚无管理员时必需 | 创建首个管理员账号 |
| `GATEWAY_ADMIN_PASSWORD` | 数据库尚无管理员时必需；至少 12 个字符 | 创建首个管理员密码 |
| `GATEWAY_OPEN_BROWSER` | 可选，默认 `true` | 控制启动后是否打开本地管理页面 |

可使用 `openssl rand -base64 32` 生成主密钥。`.env`、`config/config.toml`、数据库和运行日志均不应提交到版本库。

## 调用网关

在控制台中完成以下配置：

1. 新增上游渠道并填写 Base URL 与 API Key。
2. 为渠道发现或维护上游模型映射。
3. 创建公开模型并关联可用渠道。
4. 在“访问令牌”中签发客户端令牌。

随后使用 OpenAI 兼容客户端访问本服务：

```bash
curl http://127.0.0.1:8888/v1/chat/completions \
  -H "Authorization: Bearer <gateway-token>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<public-model>",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

### Copilot 客户端识别

Copilot CLI 或兼容客户端调用 `POST /v1/chat/completions`、`POST /v1/responses` 时，必须在每次请求中附带以下固定请求头：

```http
X-Github-Copilot-Integration-Id: copilot-cli
```

客户端的自定义请求头配置应等价于：

```json
{
  "X-Github-Copilot-Integration-Id": "copilot-cli"
}
```

请求头缺失或值不匹配时，网关不会将请求识别为 Copilot，会话日志中的客户端来源将按其他类型处理。该请求头仅用于网关内部客户端识别，转发请求时会自动移除，不会发送至上游渠道。

网关保持现有请求格式并代理流式或非流式响应。一次请求最多尝试三次；重试状态、传输错误、响应读取错误、HTTP 200 业务中断和熔断切换会记录在调用明细中。流式响应仅在首个有效输出 Token 前允许无感切换，已经输出内容后会终止重试以避免重复响应。

渠道与渠道模型的实时成功率只统计最近 30 分钟的上游尝试；窗口内没有调用时按 100% 计算。渠道成功率按其全部模型的成功尝试数与总尝试数汇总，模型路由会将对应渠道模型的成功率计入优先级权重、成本权重或延迟权重，同时为 0% 成功率线路保留极低探测概率。价格、效率、质量与均衡占比可独立配置且总和固定为 100%；均衡会比较当前模型候选渠道的实际占比和基础目标占比，按候选数使用 100～1000 条动态窗口做有界纠偏。完全缺少近期成功率和缓存样本的冷启动渠道会按配置权重共享 20% 探索流量；优先级加权策略下，统计、纠偏和探索都不会绕过最高优先级候选组。

## 数据与保留策略

- 请求日志、上游尝试及会话时间线固定保留最近五天，并由每日清理任务删除更早明细。
- 五天明细包含客户端原始请求与上下文、各次上游请求和响应、最终响应，属于敏感数据；每段正文最多保留 4 MiB，超过后标记为截断。
- 较早数据只保留按令牌汇总的每日统计，不保留完整请求、参数或渠道切换原因。
- 会话详情按时间从旧到新展示。历史记录没有路由元数据时，只推断同一请求内可以确认的失败；跨请求原因不会根据渠道当前状态反推。
- 渠道密钥使用 `GATEWAY_MASTER_KEY` 加密。主密钥丢失或更换后，已有渠道凭据无法恢复。

## 开发与构建

前后端联调需要同时启动 Go 服务和 Vite：

```bash
# 终端一
go run .

# 终端二
pnpm --dir frontend dev
```

Vite 将 `/api` 代理到 `http://127.0.0.1:8888`。生产构建会自动读取当前 Git 分支并在“系统设置”中展示；CI 可通过 `GATEWAY_BUILD_BRANCH` 显式指定构建分支。

交付前执行：

```bash
go run ./tools/projectctl check
go test ./...
go vet ./...
go build -buildvcs=false ./...
pnpm --dir frontend build
```

使用 `./build.sh` 构建跨平台产物。可通过 `PACKAGE_NAME=<name> ./build.sh` 指定本次产物名，否则脚本会优先使用当前业务分支生成名称。

## 项目结构

```text
.
├── cmd/                         # Wire 依赖注入定义与生成代码
├── frontend/src/
│   ├── components/              # Vue 公共组件和会话时间线
│   ├── navigation/              # 菜单与页面路由唯一注册源
│   └── pages/                   # 网关运营与系统设置页面
├── internal/api/                # 管理接口和 OpenAI 兼容入口
├── internal/gateway/            # 路由、转发、熔断、计费与日志
├── pkg/                         # 通用 API、Repository、Service 和事务能力
├── tools/projectctl/            # 项目身份生成与一致性检查
├── AGENTS.md                    # 开发、测试和安全约定
└── project.json                 # 项目身份唯一来源
```

## 免责声明

本项目按“现状”提供，不附带任何明示或暗示保证，包括但不限于适销性、特定用途适用性、稳定性、安全性、可用性或合规性保证。

使用者应自行评估本项目及所接入模型服务是否适用于目标场景，并负责身份认证、访问控制、HTTPS、密钥管理、日志与隐私保护、数据跨境、内容合规、费用控制、备份恢复和监控告警。部署、配置、数据处理、第三方服务调用及其产生的费用、损失或法律责任均由使用者自行承担。

本项目与 OpenAI 无隶属、授权或背书关系。“OpenAI”是其权利人的商标。第三方依赖和上游服务分别遵循其自身许可证、服务条款及隐私政策。
