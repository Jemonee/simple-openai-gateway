# pkg/core/tx — 事务管理

提供基于 `context.Context` 传播的多数据源事务域管理能力。

## 核心概念

```
MultiDataSource          注册表，管理多个命名数据源
  └── DataSource         单个数据库连接封装（name + *gorm.DB）
        └── DataSourceTransaction  单次事务实例（Source + *gorm.DB tx）

TransactionScope         事务域：统一管理当前调用链涉及的所有事务
```

## 快速上手

### 1. 注册数据源（启动时）

```go
mds := tx.GetMultiDataSource()
mds.Register(tx.CreateDataSource("main", gormDB))
```

### 2. 获取事务域

通过 `GetScope` 在业务函数入口处获取事务域，返回值的第三个参数 `deliver` **必须通过 `defer` 调用**，以确保 panic 时也能正确回滚。

```go
func DoSomething(ctx context.Context) error {
    ctx, scope, deliver := tx.GetScope(ctx, tx.JoinScope)
    defer deliver()

    // ... 业务逻辑 ...

    if err := someOperation(); err != nil {
        scope.Fail(err) // 标记失败，deliver 执行时会回滚
        return err
    }
    return nil
}
```

## ScopeMode 两种模式

| 模式 | 行为 | 交付权 | 适用场景 |
|------|------|--------|----------|
| `JoinScope` | 复用 ctx 中已有的事务域；若无则新建 | 加入已有域时**无**，新建时**有** | 参与外层事务，不关心提交时机 |
| `NewScope` | 始终创建新域；若有父域则失败状态向上传播 | **有** | 需要独立控制事务边界 |

## ITransactionScope 接口

```go
type ITransactionScope interface {
    // 获取或创建指定数据源的事务实例
    GetTransaction(ds *DataSource) *DataSourceTransaction
    // 标记域失败（触发最终回滚），失败状态向父域传播
    Fail(err error)
}
```

## 嵌套事务示例

```
外层服务（NewScope）  ──→  创建 scopeA，持有交付权
  内层服务（JoinScope）──→  复用 scopeA，无交付权
    内层服务 scope.Fail(err) ──→ scopeA.failed = true
  外层 defer deliver() ──→ scopeA 统一回滚所有事务
```

```go
// 外层：创建独立事务
func OuterService(ctx context.Context) error {
    ctx, scope, deliver := tx.GetScope(ctx, tx.NewScope)
    defer deliver()

    if err := InnerService(ctx); err != nil {
        scope.Fail(err)
        return err
    }
    return nil
}

// 内层：加入外层事务，不持有交付权
func InnerService(ctx context.Context) error {
    ctx, scope, deliver := tx.GetScope(ctx, tx.JoinScope)
    defer deliver() // 此处 deliver 是空操作

    if err := someDB(); err != nil {
        scope.Fail(err) // 失败状态传回外层 scope
        return err
    }
    return nil
}
```

## 从上下文读取当前 Scope

```go
scope, ok := tx.FromCtx(ctx)
if ok {
    // 当前调用链存在活跃事务域
}
```

## DataSource API

```go
ds := tx.CreateDataSource("name", gormDB)
ds.Db()                    // 获取原始 *gorm.DB
ds.GetTransaction()        // 开启一个新事务，返回 *DataSourceTransaction
ds.Check(dstx)             // 判断某事务是否属于该数据源
```
