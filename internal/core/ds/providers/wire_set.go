package providers

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Jemonee/simple-openai-gateway/pkg/core/tx"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func NewMultiDataSource() *tx.MultiDataSource {
	multiDataSource := tx.GetMultiDataSource()
	primaryDS := getPrimaryDS()
	multiDataSource.Register(primaryDS)
	return multiDataSource
}

// GetPrimaryDataSource 从 MultiDataSource 获取主数据源，供 Wire 注入使用。
func GetPrimaryDataSource(mds *tx.MultiDataSource) *tx.DataSource {
	return mds.GetSource("primary")
}

var primaryDBPath = filepath.Join(".", "data", "data.db")

func PrimaryDBPath() string {
	return primaryDBPath
}

func getPrimaryDS() *tx.DataSource {
	dbDir := filepath.Dir(primaryDBPath)
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		panic("failed to create database directory: " + err.Error())
	}
	db, err := gorm.Open(sqlite.Open(primarySQLiteDSN(primaryDBPath)), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		panic("failed to connect database: " + err.Error())
	}
	if err := configurePrimarySQLite(db); err != nil {
		panic("failed to configure sqlite pragmas: " + err.Error())
	}
	return tx.CreateDataSource("primary", db)
}

func primarySQLiteDSN(path string) string {
	return path + "?_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
}

func configurePrimarySQLite(db *gorm.DB) error {
	mode, err := queryPrimaryAutoVacuumMode(db)
	if err != nil {
		return err
	}
	if mode != 1 {
		if err := db.Exec("PRAGMA auto_vacuum = FULL").Error; err != nil {
			return err
		}
		hasTables, err := hasPrimaryUserTables(db)
		if err != nil {
			return err
		}
		if hasTables {
			if err := db.Exec("VACUUM").Error; err != nil {
				return err
			}
		}
		mode, err = queryPrimaryAutoVacuumMode(db)
		if err != nil {
			return err
		}
		if mode != 1 {
			return gorm.ErrInvalidDB
		}
	}
	if err := db.Exec("PRAGMA journal_mode = WAL").Error; err != nil {
		return err
	}
	if err := db.Exec("PRAGMA wal_autocheckpoint = 1000").Error; err != nil {
		return err
	}
	return verifyPrimarySQLiteWritePragmas(db)
}

func verifyPrimarySQLiteWritePragmas(db *gorm.DB) error {
	var journalMode string
	if err := db.Raw("PRAGMA journal_mode").Scan(&journalMode).Error; err != nil {
		return err
	}
	if journalMode != "wal" {
		return fmt.Errorf("unexpected SQLite journal_mode %q", journalMode)
	}
	var synchronous int
	if err := db.Raw("PRAGMA synchronous").Scan(&synchronous).Error; err != nil {
		return err
	}
	if synchronous != 1 {
		return fmt.Errorf("unexpected SQLite synchronous mode %d", synchronous)
	}
	return nil
}

func queryPrimaryAutoVacuumMode(db *gorm.DB) (int, error) {
	var mode int
	if err := db.Raw("PRAGMA auto_vacuum").Scan(&mode).Error; err != nil {
		return 0, err
	}
	return mode, nil
}

func hasPrimaryUserTables(db *gorm.DB) (bool, error) {
	var count int64
	err := db.Raw("SELECT COUNT(1) FROM sqlite_master WHERE type = ? AND name NOT LIKE ?", "table", "sqlite_%").Scan(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
