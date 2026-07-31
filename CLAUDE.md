# Repository Notes

执行任何修改前先完整阅读 `AGENTS.md`。其中定义了 Git 分支策略、环境检测、依赖安装失败处理、构建验证和项目个性化规则。

## Architecture

- `main.go` 通过 `go:embed` 打包 `frontend/dist/`，因此前端必须先构建。
- `internal/app/` 管理 Gin 服务与 SPA 静态资源。
- `internal/api/` 中的控制器实现 `pkg/api.IApi`，并在 `internal/api/providers/` 聚合。
- `internal/config/` 负责 `config/config.toml` 的生成、补全与保存。
- `internal/projectmeta/` 和 `frontend/src/config/` 是由 `project.json` 生成的元数据，禁止手工修改。
- `pkg/repository/`、`pkg/service/` 与 `pkg/core/tx/` 提供泛型数据访问和事务基础能力。
- `cmd/wire.go` 是依赖注入源文件，`cmd/wire_gen.go` 是生成结果。

## Backend Patterns

- API 成功和失败响应统一使用 `common.S` 与 `common.F`。
- Handler 首行使用 `defer a.DeferPanicHandler(c)`，并将 `c.Request.Context()` 传入 Service。
- Service 方法接收 `context.Context`，通过 `Exec(ctx)` 获得事务感知的 Repository 执行器。
- 数据模型使用 `Valid` 字段软删除，基础模型负责雪花 ID 和时间字段。
- 新增 Provider 后执行 `go generate ./cmd`，不要直接修改生成文件的业务逻辑。

## Project Identity

项目身份只能在 `project.json` 中修改。修改后运行：

```bash
go run ./tools/projectctl generate
go run ./tools/projectctl check
```

连同 Go module 重命名时使用 `projectctl rename`，不要手工批量替换 import。

## Validation

```bash
pnpm --dir frontend build
go run ./tools/projectctl check
go test ./...
go vet ./...
go build -buildvcs=false ./...
```

代码注释、提交信息和面向项目维护者的补充说明优先使用中文；公共标识符遵循对应语言的命名规范。
