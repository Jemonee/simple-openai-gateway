# Repository Guidelines

## 用户授权边界（最高优先级）

除非用户在当前任务中明确授权，否则不得新增需求范围之外的功能、页面、接口、配置、脚本、文档或其他内容。发现可改进项时只报告，不得自行实施。

运行环境默认只读。除非用户针对具体操作明确授权，否则严禁修改运行环境中的数据库数据，包括执行迁移、回填、修复、更新、插入、删除、清理、导入或恢复数据。用户授权修改源码或编写数据库迁移，不等于授权把迁移应用到运行环境；应用运行环境数据变更必须单独取得明确授权。

除非用户明确授权，否则不得停止、重启或终止运行中的服务，不得替换运行环境二进制、部署构建产物或修改运行配置。构建和测试授权不等于部署授权。只读检查不得产生隐式写入；如果工具可能触发迁移、WAL 变更或其他状态变化，必须先请求授权。

不得使用测试、模拟或临时服务占用、覆盖、代理或替换项目运行时服务及其默认端口，即使端口探测时暂时空闲。验证需要启动服务时，必须使用隔离的临时端口，避免影响用户运行中的程序，并在验证结束后关闭由代理启动的进程。

## 项目结构

这是一个 Go 与 Vue 3 单仓库项目。`main.go` 嵌入 `frontend/dist/` 并启动 Gin 服务；后端业务代码放在 `internal/`，可复用的 API、模型、Repository、Service、事务和日志能力放在 `pkg/`。Wire 定义位于 `cmd/wire.go`，生成文件为 `cmd/wire_gen.go`。

前端源码位于 `frontend/src/`，页面位于 `frontend/src/pages/`，公共资源位于 `frontend/public/`。功能菜单和页面路由统一由 `frontend/src/navigation/index.ts` 管理。项目身份只在 `project.json` 中维护，`internal/projectmeta/` 和 `frontend/src/config/project.generated.js` 是生成文件，不得直接编辑。

## Git 工作流

修改前执行 `git status --short --branch`，保留用户已有修改，不得擅自覆盖或回退。

如果系统支持 Git 但仓库不存在 `.git/`，可建立稳定基线：

```bash
git init -b main
git add .
git commit -m "chore: initialize project"
```

已有仓库不得重新初始化。独立分支是建议而非强制：小型修改或用户明确要求时可留在当前分支；高风险、跨模块工作可建议使用 `feature/<name>`、`fix/<name>` 或 `chore/<name>`。提交应聚焦，优先使用 `feat:`、`fix:`、`docs:`、`chore:` 等简洁前缀。

## 快速开始

### 1. 检测环境

安装项目依赖前先执行：

```bash
git --version
go version
node --version
corepack --version
pnpm --version
```

版本要求：Go `1.25.4`、Node `^20.19.0 || >=22.12.0`、pnpm `10.23.0`，推荐 Node 22 LTS。命令缺失或版本不满足时，先停止项目依赖安装。使用 `uname -srm`、`cat /etc/os-release` 或 Windows `systeminfo` 确认系统和 CPU 架构。

### 2. 安装缺失工具

系统级安装、全局包修改、管理员权限和代理修改必须先向用户说明目的并获得批准。

macOS：

```bash
brew install git go node
```

Debian/Ubuntu 的 Git：

```bash
sudo apt-get update
sudo apt-get install -y git
```

Fedora 的 Git：

```bash
sudo dnf install -y git
```

Go `1.25.4` 优先使用 [go.dev/dl](https://go.dev/dl/) 官方包；Node 使用已有 nvm/Volta 或 Node 22 LTS 官方包。Windows 可执行：

```powershell
winget install --id Git.Git -e
winget install --id GoLang.Go -e
winget install --id OpenJS.NodeJS.LTS -e
```

安装 Go 压缩包前必须核对架构和 SHA-256；修改 `/usr/local` 或系统 PATH 前请求批准。禁止静默使用 `sudo`、未经检查的 `curl | sh`、关闭 TLS 校验或切换到不可信镜像。

### 3. 安装 pnpm

```bash
corepack enable
corepack install --global pnpm@10.23.0
pnpm --version
```

如果 `corepack enable` 因目录权限失败，不使用 `sudo` 覆盖；临时使用 `corepack pnpm <command>`，或提示用户修复 Node 安装权限。Corepack 缺失时，取得批准后再安装固定版本。

### 4. 安装项目依赖

```bash
go mod download
cd frontend
pnpm install --frozen-lockfile
pnpm build
cd ..
```

必须使用 lockfile，不得通过删除 lockfile、关闭完整性检查或随意升级依赖绕过失败。Go 使用 `go:embed` 嵌入 `frontend/dist/`，因此首次运行前必须完成前端构建。

### 5. 开发、运行与打包

完整运行：

```bash
pnpm --dir frontend build
go run .
```

默认监听 `0.0.0.0:8888`，每个进程只调用一次系统默认浏览器并打开 `http://127.0.0.1:8888/static/`。`0.0.0.0` 只用于监听，不是浏览器访问地址。

前后端联调：

```bash
# 终端一
go run .

# 终端二
pnpm --dir frontend dev
```

Vite 将 `/api` 代理到 `http://127.0.0.1:8888`。只运行前端开发服务时后端 API 不可用，联调必须同时运行 Go 服务。打包命令：

```bash
pnpm --dir frontend build
./build.sh
```

不得假定固定产物名。用户指定名称时执行：

```bash
PACKAGE_NAME=<package-slug> ./build.sh
```

未指定时，`build.sh` 从有业务含义的分支名生成，例如 `feature/device-monitoring` 生成 `device-monitoring`。`main`、`master`、detached HEAD 或无法转换的分支名回退到 `project.json.binaryName`。打包完成后向用户报告实际产物名。修改 Wire provider 后执行 `go generate ./cmd`。

## 依赖安装失败处理

按以下顺序定位：命令与 PATH、实际版本、目录写权限、DNS/代理/证书/仓库连通性、系统与 CPU 架构、Go module cache 或 pnpm store 离线缓存。修复一个明确原因后最多重试一次，不循环安装，不擅自更换镜像。企业代理和私有镜像地址必须由用户提供，不永久修改全局代理。

无法继续时停止后续构建，并按统一格式报告：

```text
Dependency setup is blocked: <dependency> <required-version>.
Attempted: <command>
Error: <key error and exit code>
Tried: <diagnostic or remediation>
Required action: <approval, proxy information, or manual installation>
Resume with: <verification command>
Unverified checks: <build/test list>
```

## 功能菜单与页面注册

所有新增的用户功能都必须表现为可访问的功能菜单；新增页面必须直接挂载到菜单树，不得只在 Vue Router 中添加孤立路由。`frontend/src/navigation/index.ts` 是菜单与页面路由的唯一注册源，`frontend/src/router/index.ts` 只消费自动生成的 `navigationRoutes`。

新增页面时必须在 `navigationGroups` 中声明唯一 `key`、`label`、`icon`、`path`、`routeName` 和懒加载 `component`。通过 `children` 挂到正确父菜单；`children` 支持递归多级。可访问的叶子缺少路由字段时应用构建会失败。示例：

```ts
{
  key: 'device-monitoring',
  path: '/device-monitoring',
  routeName: 'device-monitoring',
  label: '设备监控',
  icon: Monitor,
  component: () => import('@/pages/DeviceMonitoring.vue'),
}
```

菜单遵循“配置即显示”：登记在 `navigationGroups` 中的菜单全部直接展示和访问，不增加角色、权限、租户、隐藏字段、灰度开关、功能开关或二次装配。这个项目用于快速验证原型，不为菜单引入权限系统。菜单搜索只做用户主动的文本筛选；访问子页面时父菜单自动展开，面包屑反映完整层级。删除或移动页面时同步修改菜单树。纯后端支撑、重构或基础设施工作不伪造菜单。

## 项目个性化

Go module、二进制名、应用名、显示名称、描述、版本、令牌前缀和 favicon 只在 `project.json` 维护。单次打包产物名不属于项目身份。设置页中的仓库链接是固定指向本仓库的例外。

修改清单后执行：

```bash
go run ./tools/projectctl generate
go run ./tools/projectctl check
```

完整重命名使用：

```bash
go run ./tools/projectctl rename \
  --module <module-path> \
  --binary <binary-name> \
  --app <application-name> \
  --display "<display name>" \
  --description "<application description>" \
  --token-prefix <token_prefix> \
  --favicon /favicon.svg
```

现有 `config/config.toml` 和令牌不会迁移；品牌资源变化时需替换对应 favicon。

## 编码与测试规范

Go 使用 `gofmt`、Tab 缩进和标准命名；API 实现 `pkg/api.IApi`。业务实现放在 `internal/`，可复用能力放在 `pkg/`。Vue 组件使用 PascalCase，JavaScript/TypeScript 标识符使用 camelCase 和两空格缩进。不得手工修改 `frontend/dist/`、生成元数据或 `cmd/wire_gen.go`。

Go 测试与源码同目录，命名为 `*_test.go` 和 `TestXxx`。当前没有前端单元测试运行器，前端修改必须完成生产构建和实际路由检查。交付前执行：

```bash
go run ./tools/projectctl check
go test ./...
go vet ./...
go build -buildvcs=false ./...
pnpm --dir frontend build
```

## UI 方向

编辑 Vue 页面、应用外壳或样式前，完整读取 `docs/design/hongfen/THEME.md`。未指定其他风格时，延续 `frontend/src/assets/hongfen-theme.css` 定义的“红粉”主题。现有页面只演示功能，布局可以按业务重做，但必须响应式、可访问，并覆盖加载、空数据、错误和成功状态。

## 需求提示词模板

开始任务前读取对应模板：

- 功能开发：`docs/prompts/feature-request.md`
- 模块开发：`docs/prompts/module-request.md`
- 页面调整：`docs/prompts/page-change-request.md`

模板用于发现会影响实现的缺失信息，不是强制表单。用户信息充分时直接实施；信息不足时只询问会改变实现的字段，可向用户提供对应模板路径。独立分支仍为可选项。

## Pull Request 与安全

Pull Request 应说明行为变化、验证命令、关联 issue，并为可见 UI 修改提供截图。不得混入无关重构或生成构建产物。

运行配置和令牌位于被忽略的 `config/config.toml`。禁止提交配置文件、访问令牌、共享令牌、数据库或日志。`/api/settings` 会读写令牌，相关修改按安全敏感变更处理。
