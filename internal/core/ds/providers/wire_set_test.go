package providers

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPrimarySQLiteWritePragmas(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "primary.db")
	db, err := gorm.Open(sqlite.Open(primarySQLiteDSN(databasePath)), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := configurePrimarySQLite(db); err != nil {
		t.Fatal(err)
	}

	var journalMode string
	if err := db.Raw("PRAGMA journal_mode").Scan(&journalMode).Error; err != nil || journalMode != "wal" {
		t.Fatalf("journal_mode = %q, err = %v", journalMode, err)
	}
	var synchronous int
	if err := db.Raw("PRAGMA synchronous").Scan(&synchronous).Error; err != nil || synchronous != 1 {
		t.Fatalf("synchronous = %d, err = %v", synchronous, err)
	}
	var busyTimeout int
	if err := db.Raw("PRAGMA busy_timeout").Scan(&busyTimeout).Error; err != nil || busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, err = %v", busyTimeout, err)
	}
	var autoVacuum int
	if err := db.Raw("PRAGMA auto_vacuum").Scan(&autoVacuum).Error; err != nil || autoVacuum != 1 {
		t.Fatalf("auto_vacuum = %d, err = %v", autoVacuum, err)
	}
}
