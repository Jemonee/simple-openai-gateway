package repository

import (
	"github.com/Jemonee/simple-openai-gateway/pkg/common"
	"github.com/Jemonee/simple-openai-gateway/pkg/core/tx"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const defaultWriteBatchSize = 50

// IExecutor CRUD 执行能力，可绑定到默认 DB 或特定事务 DB。
type IExecutor[T any] interface {
	Create(entity *T) error
	CreateBatch(entities []T) error
	UpsertBatch(entities []T, onConflict clause.OnConflict) error
	Update(entity *T) error
	DeleteById(id uint) error
	Delete(query interface{}, args ...interface{}) error
	List() ([]T, *gorm.DB)
	Page(sortParams *string, page common.Page[T]) (common.Page[T], error)
	One(sortParams *string, query interface{}, args ...interface{}) (T, error)
	SelectList(sortParams *string, query interface{}, args ...interface{}) ([]T, error)
	SelectPage(sortParams *string, page common.Page[T], query interface{}, args ...interface{}) (common.Page[T], error)
	Where(query interface{}, args ...interface{}) *gorm.DB
	Db() *gorm.DB
}

// IRepository 完整的仓库能力：包含 CRUD 执行、元数据方法及副本创建。
type IRepository[T any] interface {
	IExecutor[T]
	GetEntity() *T
	DefaultSort() string
	GetSort(sortParams *string) string
	InitializeRepository()
	// WithScope 以事务域中对应数据源的事务创建当前 Repository 的副本（IExecutor）。
	WithScope(scope tx.ITransactionScope) IExecutor[T]
	// WithDb 以指定 *gorm.DB 创建当前 Repository 的副本（IExecutor）。
	WithDb(db *gorm.DB) IExecutor[T]
}

// BaseRepository 提供 IRepository 的默认实现。
//
// 默认使用注册的 DataSource 进行数据库操作。
// 通过 WithScope 或 WithDb 可获得绑定到特定事务/DB 的轻量副本，
// 副本仅实现 IExecutor，不会影响原实例。
type BaseRepository[T any] struct {
	dataSource *tx.DataSource
	db         *gorm.DB // 非 nil 时优先于 dataSource.Db()
}

var _ IRepository[any] = (*BaseRepository[any])(nil)

// NewBaseRepository 构造器，ds 为启动时注册的数据源。
func NewBaseRepository[T any](ds *tx.DataSource) *BaseRepository[T] {
	return &BaseRepository[T]{dataSource: ds}
}

// WithScope 以事务域中对应数据源的事务创建副本，副本与原实例相互独立。
func (br *BaseRepository[T]) WithScope(scope tx.ITransactionScope) IExecutor[T] {
	dst := scope.GetTransaction(br.dataSource)
	clone := *br
	clone.db = dst.Tx
	return &clone
}

// WithDb 以指定的 *gorm.DB 创建副本，副本与原实例相互独立。
func (br *BaseRepository[T]) WithDb(db *gorm.DB) IExecutor[T] {
	clone := *br
	clone.db = db
	return &clone
}

// Db 返回当前使用的 *gorm.DB（事务 DB 优先，否则返回数据源默认 DB）。
func (br *BaseRepository[T]) Db() *gorm.DB {
	return br.getDB()
}

// getDB 返回当前生效的 *gorm.DB：副本模式下返回绑定的 db，默认模式下返回数据源 DB。
func (br *BaseRepository[T]) getDB() *gorm.DB {
	if br.db != nil {
		return br.db
	}
	if br.dataSource == nil {
		panic("dataSource is nil")
	}
	return br.dataSource.Db()
}

func (br *BaseRepository[T]) GetEntity() *T {
	return new(T)
}

func (br *BaseRepository[T]) DefaultSort() string {
	return "sort asc"
}

func (br *BaseRepository[T]) GetSort(sortParams *string) string {
	if sortParams != nil && *sortParams != "" {
		return *sortParams
	}
	return br.DefaultSort()
}

func (br *BaseRepository[T]) InitializeRepository() {
	// AutoMigrate 会按模型上的 gorm tag 同步表结构、缺失列与索引定义。
	if err := br.dataSource.Db().AutoMigrate(br.GetEntity()); err != nil {
		panic(err)
	}
}

func (br *BaseRepository[T]) Where(query interface{}, args ...interface{}) *gorm.DB {
	return br.getDB().Model(br.GetEntity()).Where(query, args...)
}

func (br *BaseRepository[T]) Create(entity *T) error {
	return br.getDB().Create(entity).Error
}

func (br *BaseRepository[T]) CreateBatch(entities []T) error {
	return br.writeInBatches(br.getDB(), entities)
}

func (br *BaseRepository[T]) UpsertBatch(entities []T, onConflict clause.OnConflict) error {
	return br.writeInBatches(br.getDB().Clauses(onConflict), entities)
}

func (br *BaseRepository[T]) Update(entity *T) error {
	return br.getDB().Save(entity).Error
}

func (br *BaseRepository[T]) Delete(query interface{}, args ...interface{}) error {
	return br.Where(query, args...).Update("Valid", 0).Error
}

func (br *BaseRepository[T]) DeleteById(id uint) error {
	return br.Where("`id` = ?", id).Update("Valid", 0).Error
}

func (br *BaseRepository[T]) writeInBatches(db *gorm.DB, entities []T) error {
	if len(entities) == 0 {
		return nil
	}
	return db.CreateInBatches(entities, defaultWriteBatchSize).Error
}

// List 获取所有有效数据，使用默认排序。
func (br *BaseRepository[T]) List() ([]T, *gorm.DB) {
	var res []T
	db := br.Where("`Valid` = ?", 1).Order(br.DefaultSort()).Find(&res)
	return res, db
}

// One 按条件查询首条数据。
func (br *BaseRepository[T]) One(sortParams *string, query interface{}, args ...interface{}) (T, error) {
	var res T
	db := br.Where(query, args...).Order(br.GetSort(sortParams)).First(&res)
	return res, db.Error
}

// SelectList 按条件查询列表数据。
func (br *BaseRepository[T]) SelectList(sortParams *string, query interface{}, args ...interface{}) ([]T, error) {
	var res []T
	db := br.Where(query, args...).Order(br.GetSort(sortParams)).Find(&res)
	return res, db.Error
}

// Page 分页查询所有有效数据。
func (br *BaseRepository[T]) Page(sortParams *string, page common.Page[T]) (common.Page[T], error) {
	var res []T
	sort := br.GetSort(sortParams)
	var count int64
	query := br.Where("`Valid` = ?", 1)
	query.Count(&count)
	page.Total = count
	first := page.GetFirst()
	if count < int64(first) {
		page.PageNo = 1
		first = page.GetFirst()
	}
	db := query.Order(sort).Offset(first).Limit(page.PageSize).Find(&res)
	if db.Error != nil {
		return page, db.Error
	}
	page.Result = res
	return page, nil
}

// SelectPage 按条件分页查询列表数据。
func (br *BaseRepository[T]) SelectPage(sortParams *string, page common.Page[T], query interface{}, args ...interface{}) (common.Page[T], error) {
	var res []T
	sort := br.GetSort(sortParams)
	var count int64
	br.Where(query, args...).Count(&count)
	page.Total = count
	first := page.GetFirst()
	if count < int64(first) {
		page.PageNo = 1
		first = page.GetFirst()
	}
	db := br.Where(query, args...).Order(sort).Offset(first).Limit(page.PageSize).Find(&res)
	if db.Error != nil {
		return page, db.Error
	}
	page.Result = res
	return page, nil
}
