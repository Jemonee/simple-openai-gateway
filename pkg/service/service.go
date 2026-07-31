package service

import (
	"context"
	"github.com/Jemonee/simple-openai-gateway/pkg/common"
	"github.com/Jemonee/simple-openai-gateway/pkg/core/tx"
	"github.com/Jemonee/simple-openai-gateway/pkg/repository"
)

// IBaseService[T] 服务基础能力接口，T 为绑定的业务模型类型。
type IBaseService[T any] interface {
	// GetById 按主键查询单条记录。
	GetById(ctx context.Context, id uint) (T, error)
	// List 查询所有有效记录，使用默认排序。
	List(ctx context.Context) ([]T, error)
	// SelectList 按条件查询记录列表。
	SelectList(ctx context.Context, sortParams *string, query interface{}, args ...interface{}) ([]T, error)
	// Page 分页查询所有有效记录。
	Page(ctx context.Context, sortParams *string, page common.Page[T]) (common.Page[T], error)
	// SelectPage 按条件分页查询记录列表。
	SelectPage(ctx context.Context, sortParams *string, page common.Page[T], query interface{}, args ...interface{}) (common.Page[T], error)
}

// BaseService[T] 提供 IBaseService[T] 的默认实现。
type BaseService[T any] struct {
	Repo repository.IRepository[T]
}

var _ IBaseService[any] = (*BaseService[any])(nil)

func NewBaseService[T any](repo repository.IRepository[T]) *BaseService[T] {
	return &BaseService[T]{Repo: repo}
}

// joinTx 以 JoinScope 模式获取事务域。调用方不持有交付权。
func (s *BaseService[T]) joinTx(ctx context.Context) (context.Context, tx.ITransactionScope, func()) {
	return tx.GetScope(ctx, tx.JoinScope)
}

// newTx 以 NewScope 模式创建新事务域。调用方持有交付权，必须 defer 返回的第三个值。
func (s *BaseService[T]) newTx(ctx context.Context) (context.Context, tx.ITransactionScope, func()) {
	return tx.GetScope(ctx, tx.NewScope)
}

// Exec 返回感知当前上下文事务的执行器：
// ctx 中有活跃事务域则返回绑定事务的副本，否则返回默认 DB 的仓库。
func (s *BaseService[T]) Exec(ctx context.Context) repository.IExecutor[T] {
	if scope, ok := tx.FromCtx(ctx); ok {
		return s.Repo.WithScope(scope)
	}
	return s.Repo
}

func (s *BaseService[T]) GetById(ctx context.Context, id uint) (T, error) {
	return s.Exec(ctx).One(nil, "`id` = ?", id)
}

func (s *BaseService[T]) List(ctx context.Context) ([]T, error) {
	res, db := s.Exec(ctx).List()
	return res, db.Error
}

func (s *BaseService[T]) SelectList(ctx context.Context, sortParams *string, query interface{}, args ...interface{}) ([]T, error) {
	return s.Exec(ctx).SelectList(sortParams, query, args...)
}

func (s *BaseService[T]) Page(ctx context.Context, sortParams *string, page common.Page[T]) (common.Page[T], error) {
	return s.Exec(ctx).Page(sortParams, page)
}

func (s *BaseService[T]) SelectPage(ctx context.Context, sortParams *string, page common.Page[T], query interface{}, args ...interface{}) (common.Page[T], error) {
	return s.Exec(ctx).SelectPage(sortParams, page, query, args...)
}
