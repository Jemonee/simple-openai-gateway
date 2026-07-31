package tx

import (
	"github.com/Jemonee/simple-openai-gateway/pkg/until"
	"sync"

	"gorm.io/gorm"
)

var log = until.Log

type DataSource struct {
	name string
	db   *gorm.DB
}

func CreateDataSource(name string, db *gorm.DB) *DataSource {
	return &DataSource{
		name: name,
		db:   db,
	}
}

func (ds *DataSource) Db() *gorm.DB {
	return ds.db
}

func (ds *DataSource) Check(transaction *DataSourceTransaction) bool {
	if transaction != nil {
		return transaction.Source == ds.name
	}
	return false
}

func (ds *DataSource) GetTransaction() *DataSourceTransaction {
	newTx := ds.db.Begin()
	return NewDataSourceTransaction(ds.name, newTx)
}

// MultiDataSource 多数据源管理器
type MultiDataSource struct {
	mu        sync.Mutex
	sourceMap map[string]*DataSource
}

func GetMultiDataSource() *MultiDataSource {
	return &MultiDataSource{
		sourceMap: make(map[string]*DataSource),
	}
}

func (ds *MultiDataSource) Drop(name string) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	_, ok := ds.sourceMap[name]
	if !ok {
		panic("invalid data source name")
	}
	delete(ds.sourceMap, name)
}

// Register 注册数据源，重复注册同名数据源时 panic。
func (ds *MultiDataSource) Register(source *DataSource) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if _, ok := ds.sourceMap[source.name]; ok {
		panic("duplicate register data source: " + source.name)
	}
	ds.sourceMap[source.name] = source
	log.Infof("register data source [%s]", source.name)
}

func (ds *MultiDataSource) GetSource(name string) *DataSource {
	dataSource, ok := ds.sourceMap[name]
	if ok {
		return dataSource
	}
	log.Errorf("not found source %s", name)
	panic("data source not found: " + name)
}
