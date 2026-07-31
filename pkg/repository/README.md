# pkg/repository — 数据仓库基类

提供泛型 Repository 基类，封装常见 CRUD 操作，支持默认 DB 与事务副本两种执行模式。

## 接口层级

```
IRepository[T]          完整仓库能力（含副本创建）
  └── IExecutor[T]      CRUD 执行能力（可绑定到默认 DB 或事务）
```

### IExecutor[T]

| 方法 | 说明 |
|------|------|
| `Create(entity *T) error` | 插入记录 |
| `Update(entity *T) error` | 全字段更新（Save） |
| `DeleteById(id uint) error` | 按主键软删除（Valid=0） |
| `Delete(query, args...) error` | 按条件软删除 |
| `List() ([]T, *gorm.DB)` | 查询所有有效记录（Valid=1） |
| `One(sort, query, args...) (T, error)` | 按条件查首条 |
| `SelectList(sort, query, args...) ([]T, error)` | 按条件查列表 |
| `Page(sort, page) (Page[T], error)` | 全量分页 |
| `SelectPage(sort, page, query, args...) (Page[T], error)` | 条件分页 |
| `Where(query, args...) *gorm.DB` | 返回带条件的 *gorm.DB，可继续链式调用 |
| `Db() *gorm.DB` | 获取当前生效的 *gorm.DB |

### IRepository[T]（扩展 IExecutor[T]）

| 方法 | 说明 |
|------|------|
| `WithScope(scope ITransactionScope) IExecutor[T]` | 以事务域创建副本 |
| `WithDb(db *gorm.DB) IExecutor[T]` | 以指定 DB 创建副本 |
| `InitializeRepository()` | AutoMigrate 建表（启动时调用） |
| `DefaultSort() string` | 默认排序字段，可覆盖 |
| `GetSort(sortParams *string) string` | 解析排序参数 |
| `GetEntity() *T` | 返回模型零值指针 |

## 快速上手

### 1. 定义 Repository

```go
type WorkSpaceRepository struct {
    repository.BaseRepository[WorkSpace]
}

func NewWorkSpaceRepository(ds *tx.DataSource) *WorkSpaceRepository {
    return &WorkSpaceRepository{
        BaseRepository: *repository.NewBaseRepository[WorkSpace](ds),
    }
}

// 覆盖默认排序
func (r *WorkSpaceRepository) DefaultSort() string {
    return "created_at desc"
}

// 自定义查询方法
func (r *WorkSpaceRepository) FindByName(name string) ([]WorkSpace, error) {
    return r.SelectList(nil, "`name` = ?", name)
}
```

### 2. 初始化（启动时建表）

```go
repo := NewWorkSpaceRepository(dataSource)
repo.InitializeRepository()
```

### 3. 基础 CRUD

```go
// 创建
err := repo.Create(&WorkSpace{Name: "test"})

// 查询
ws, err := repo.One(nil, "`name` = ?", "test")

// 列表
list, err := repo.SelectList(nil, "`status` = ?", 1)

// 分页
sort := "created_at desc"
page, err := repo.Page(&sort, common.Page[WorkSpace]{PageNo: 1, PageSize: 20})

// 更新
err = repo.Update(&ws)

// 软删除
err = repo.DeleteById(ws.Id)
```

## 事务副本

当需要在事务中执行操作时，通过 `WithScope` 或 `WithDb` 获取绑定事务的副本，**副本与原实例相互独立**。

### WithScope（推荐）

```go
func (s *WorkSpaceService) Create(ctx context.Context, ws *WorkSpace) error {
    ctx, scope, deliver := tx.GetScope(ctx, tx.JoinScope)
    defer deliver()

    // 获取绑定当前事务的副本
    exec := s.repo.WithScope(scope)
    if err := exec.Create(ws); err != nil {
        scope.Fail(err)
        return err
    }
    return nil
}
```

### WithDb（直接指定 DB）

```go
db := gormDB.Begin()
exec := repo.WithDb(db)
exec.Create(&ws)
db.Commit()
```

## 注意事项

- 软删除通过 `Valid` 字段实现（`Valid=1` 有效，`Valid=0` 已删除）
- `List` 和 `Page` 自动过滤 `Valid=0` 的记录
- `WithScope` / `WithDb` 返回的副本**不持有**数据源引用，不可调用 `InitializeRepository`
- `sortParams` 参数传 `nil` 时使用 `DefaultSort()`
