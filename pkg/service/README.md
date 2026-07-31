# pkg/service — 业务服务基类

提供泛型 Service 基类，内置事务管理与常见查询方法，子类只需关注业务逻辑。

## 接口

```go
type IBaseService[T any] interface {
    JoinTx(ctx)                                          // 加入已有事务域
    NewTx(ctx)                                           // 创建新事务域
    GetById(ctx, id) (T, error)                          // 按主键查询
    List(ctx) ([]T, error)                               // 查询所有有效记录
    SelectList(ctx, sort, query, args...) ([]T, error)   // 按条件查列表
    Page(ctx, sort, page) (Page[T], error)               // 全量分页
    SelectPage(ctx, sort, page, query, args...) (Page[T], error) // 条件分页
}
```

## 快速上手

### 1. 定义 Service

```go
type WorkSpaceService struct {
    service.BaseService[WorkSpace]
}

func NewWorkSpaceService(repo *WorkSpaceRepository) *WorkSpaceService {
    return &WorkSpaceService{
        BaseService: *service.NewBaseService[WorkSpace](repo),
    }
}
```

### 2. 继承查询方法（无需额外实现）

```go
svc := NewWorkSpaceService(repo)

// 直接使用继承的查询方法
ws, err := svc.GetById(ctx, 1001)
list, err := svc.List(ctx)

sort := "created_at desc"
page, err := svc.Page(ctx, &sort, common.Page[WorkSpace]{PageNo: 1, PageSize: 10})

list, err = svc.SelectList(ctx, nil, "`status` = ?", 1)
```

### 3. 自定义写操作（使用事务）

```go
func (s *WorkSpaceService) Create(ctx context.Context, ws *WorkSpace) error {
    ctx, scope, deliver := s.JoinTx(ctx)  // 加入外层事务（若有）
    defer deliver()

    if err := s.Repo.WithScope(scope).Create(ws); err != nil {
        scope.Fail(err)
        return err
    }
    return nil
}
```

### 4. 需要独立事务时使用 NewTx

```go
func (s *WorkSpaceService) Transfer(ctx context.Context, from, to uint) error {
    // NewTx 始终创建新域，不受外层事务影响
    ctx, scope, deliver := s.NewTx(ctx)
    defer deliver()

    exec := s.Repo.WithScope(scope)
    if err := exec.DeleteById(from); err != nil {
        scope.Fail(err)
        return err
    }
    if err := exec.Create(&WorkSpace{Id: to}); err != nil {
        scope.Fail(err)
        return err
    }
    return nil
}
```

## 事务感知查询

所有继承的查询方法（`GetById`、`List`、`Page` 等）会自动感知上下文中的事务：

```
ctx 中存在活跃事务域  →  使用事务 DB 执行查询（保证事务内读一致性）
ctx 中无事务域        →  使用注册的默认 DB 执行查询
```

这意味着在事务函数中调用查询方法，**不需要手动传 scope**，自动参与当前事务：

```go
func (s *WorkSpaceService) CreateAndVerify(ctx context.Context, ws *WorkSpace) error {
    ctx, scope, deliver := s.JoinTx(ctx)
    defer deliver()

    if err := s.Repo.WithScope(scope).Create(ws); err != nil {
        scope.Fail(err)
        return err
    }

    // GetById 自动使用当前事务 DB，可读取到刚插入但未提交的数据
    created, err := s.GetById(ctx, ws.Id)
    if err != nil {
        scope.Fail(err)
        return err
    }
    _ = created
    return nil
}
```

## 两种事务模式对比

| | `JoinTx` | `NewTx` |
|---|---|---|
| **行为** | 复用已有域；无则新建 | 始终新建 |
| **交付权** | 加入已有时无，新建时有 | 始终有 |
| **失败传播** | 失败会影响外层事务 | 失败向父域传播 |
| **适用场景** | 普通业务方法，参与外层事务 | 需要独立事务边界的操作 |

## 访问 Repo

子类可通过 `s.Repo` 直接访问仓库，调用自定义查询方法：

```go
func (s *WorkSpaceService) FindByName(ctx context.Context, name string) ([]WorkSpace, error) {
    // 将 Repo 断言为具体类型以调用自定义方法
    return s.Repo.(*WorkSpaceRepository).FindByName(name)
}
```
