package tx

import (
	"time"

	"gorm.io/gorm"
)

type DataSourceTransaction struct {
	Source     string
	Tx         *gorm.DB
	CreateTime time.Time
	failed     bool
	err        *error
}

func (dst *DataSourceTransaction) makeFailed(err error) {
	dst.failed = true
	dst.err = &err
}

func (dst *DataSourceTransaction) IsFailed() bool {
	return dst.failed
}

func NewDataSourceTransaction(source string, tx *gorm.DB) *DataSourceTransaction {
	return &DataSourceTransaction{
		Source:     source,
		Tx:         tx,
		CreateTime: time.Now(),
		failed:     false,
		err:        nil,
	}
}
