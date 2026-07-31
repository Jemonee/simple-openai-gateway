package tx

import (
	"context"
	"fmt"
	"sync"
)

type scopeKeyType struct{}

var scopeKey = scopeKeyType{}

type ScopeMode = int

const (
	JoinScope ScopeMode = 2 << iota
	NewScope
)

// ITransactionScope 事务域对外暴露的能力。
type ITransactionScope interface {
	// GetTransaction 获取当前域内指定数据源的事务，不存在则自动开启并托管。
	GetTransaction(ds *DataSource) *DataSourceTransaction
	// Fail 将当前域标记为失败，触发最终回滚；失败状态会向父域传播。
	Fail(err error)
}

// TransactionScope 是 ITransactionScope 的默认实现。
//
// 设计原则：
//   - 谁创建谁交付：GetScope 返回的 cleanup func 由创建者通过 defer 调用。
//   - JoinScope 加入已有域时不持有交付权，cleanup 为空操作。
//   - 同一域内所有数据源事务统一提交或统一回滚，不允许部分成功。
//   - 子域失败自动向父域传播，确保嵌套事务一致性。
type TransactionScope struct {
	mu         sync.Mutex
	parent     *TransactionScope
	managedTxs map[string]*DataSourceTransaction
	errs       []error
	failed     bool
	delivered  bool
}

var _ ITransactionScope = (*TransactionScope)(nil)

// GetTransaction 获取该数据源在当前域内的事务实例，不存在则开启新事务并托管。
func (ts *TransactionScope) GetTransaction(ds *DataSource) *DataSourceTransaction {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if tx, ok := ts.managedTxs[ds.name]; ok {
		return tx
	}
	tx := ds.GetTransaction()
	ts.managedTxs[tx.Source] = tx
	return tx
}

// Fail 将当前域标记为失败，并向父域传播。
func (ts *TransactionScope) Fail(err error) {
	ts.mu.Lock()
	ts.failed = true
	if err != nil {
		ts.errs = append(ts.errs, err)
	}
	parent := ts.parent
	ts.mu.Unlock()

	if parent != nil {
		parent.Fail(err)
	}
}

// deliver 执行实际的提交或回滚。
// 必须通过 defer 调用，以确保能捕获 panic。
func (ts *TransactionScope) deliver() {
	// 捕获 panic 并标记失败，保证事务不会在异常时意外提交
	if r := recover(); r != nil {
		ts.Fail(fmt.Errorf("panic: %v", r))
	}

	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.delivered {
		return
	}
	ts.delivered = true

	// 检查各事务自身是否已标记失败
	for _, tx := range ts.managedTxs {
		if tx.IsFailed() {
			ts.failed = true
			break
		}
	}

	// 统一提交或回滚
	for _, tx := range ts.managedTxs {
		if ts.failed {
			tx.Tx.Rollback()
		} else {
			if err := tx.Tx.Commit().Error; err != nil {
				log.Errorf("commit failed on source [%s]: %v", tx.Source, err)
			}
		}
	}
}

// FromCtx 从 ctx 中读取当前事务域，若无则返回 nil, false。
func FromCtx(ctx context.Context) (ITransactionScope, bool) {
	scope, ok := ctx.Value(scopeKey).(*TransactionScope)
	return scope, ok
}

func newScope(parent *TransactionScope) *TransactionScope {
	return &TransactionScope{
		parent:     parent,
		managedTxs: make(map[string]*DataSourceTransaction),
	}
}

// GetScope 从 ctx 中获取或创建事务域。
//
// 返回的 cleanup func 必须通过 defer 调用：
//
//	ctx, scope, deliver := GetScope(ctx, JoinScope)
//	defer deliver()
//
// JoinScope：复用 ctx 中已有的事务域，调用方不持有交付权（cleanup 为空操作）；
// 若 ctx 中无事务域，则行为等同 NewScope。
//
// NewScope：始终创建新域，调用方负责交付；若 ctx 中有父域，失败状态向父域传播。
func GetScope(ctx context.Context, mode ScopeMode) (context.Context, ITransactionScope, func()) {
	existing, _ := ctx.Value(scopeKey).(*TransactionScope)

	if mode == JoinScope && existing != nil {
		// 加入已有域，不持有交付权
		return ctx, existing, func() {}
	}

	scope := newScope(existing)
	ctx = context.WithValue(ctx, scopeKey, scope)
	return ctx, scope, scope.deliver
}
