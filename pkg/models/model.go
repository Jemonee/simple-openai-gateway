package models

import (
	"github.com/Jemonee/simple-openai-gateway/pkg/until"
	"time"

	"gorm.io/gorm"
)

type BaseModel struct {
	Id         *uint64    `gorm:"primary_key;AUTO_INCREMENT;comment:主键ID" json:"id"`
	Valid      int        `gorm:"default:1;index;comment:有效标记" json:"valid"`
	Sort       *int       `gorm:"default:0;comment:排序值" json:"sort"`
	CreateTime *time.Time `gorm:"type:datetime;not null;comment:创建时间" json:"createTime"`
	ModifyTime *time.Time `gorm:"type:datetime;not null;index;comment:修改时间" json:"modifyTime"`
}

// BeforeCreate 数据插入数据之前处理
func (e *BaseModel) BeforeCreate(tx *gorm.DB) error {
	// 单独设置默认值逻辑（如果有需要）
	if e.Id == nil {
		id, err := until.IdGenerate.NextId()
		if err != nil {
			return err
		}
		e.Id = &id
	}
	if e.Valid == 0 {
		e.Valid = 1
	}
	now := time.Now()
	if e.CreateTime == nil {
		e.CreateTime = &now
	}
	if e.ModifyTime == nil {
		e.ModifyTime = &now
	}
	return nil
}
