//go:build qrdb_sql

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gorm

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	qrdbdriver "github.com/jeremyhahn/go-qrdb/sdk/go/driver"
	_ "github.com/jeremyhahn/go-qrdb/sdk/go/driver/node"
)

// openGORM opens dsn through Open and gorm.Open, closing the pool at cleanup.
func openGORM(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	dialector, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open(%q): %v", dsn, err)
	}
	db, err := gorm.Open(dialector, &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("gorm.Open(%q): %v", dsn, err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return db
}

func TestNewConfig_SetsTheQRDBDefaults(t *testing.T) {
	cfg := NewConfig()
	if cfg.DriverName != DriverName {
		t.Errorf("DriverName = %q, want %q", cfg.DriverName, DriverName)
	}
	if cfg.DefaultStringSize != DefaultStringSize {
		t.Errorf("DefaultStringSize = %d, want %d", cfg.DefaultStringSize, DefaultStringSize)
	}
	if cfg.DefaultDatetimePrecision == nil || *cfg.DefaultDatetimePrecision != DefaultDatetimePrecision {
		t.Errorf("DefaultDatetimePrecision = %v, want %d", cfg.DefaultDatetimePrecision, DefaultDatetimePrecision)
	}
	if cfg.DSN != "" || cfg.Conn != nil {
		t.Errorf("DSN = %q, Conn = %v; NewConfig must leave both to the caller", cfg.DSN, cfg.Conn)
	}
}

func TestNewConfig_ReturnsAnUnsharedPrecision(t *testing.T) {
	first, second := NewConfig(), NewConfig()
	*first.DefaultDatetimePrecision = 0
	if *second.DefaultDatetimePrecision != DefaultDatetimePrecision {
		t.Fatalf("changing one Config's precision changed another's to %d", *second.DefaultDatetimePrecision)
	}
}

func TestOpen_RefusesNetworkSQLTransports(t *testing.T) {
	tests := []struct {
		dsn    string
		scheme string
	}{
		{dsn: "rest://db.example.com:8080/app", scheme: qrdbdriver.SchemeREST},
		{dsn: "grpc://db.example.com:9090/app", scheme: qrdbdriver.SchemeGRPC},
	}
	for _, tt := range tests {
		t.Run(tt.scheme, func(t *testing.T) {
			dialector, err := Open(tt.dsn)
			var transportErr *UnsupportedTransportError
			if !errors.As(err, &transportErr) {
				t.Fatalf("Open(%q) error = %v (%T), want *UnsupportedTransportError", tt.dsn, err, err)
			}
			if transportErr.Scheme != tt.scheme {
				t.Errorf("Scheme = %q, want %q", transportErr.Scheme, tt.scheme)
			}
			if dialector != nil {
				t.Errorf("dialector = %v, want nil on error", dialector)
			}
		})
	}
}

func TestOpen_ReportsAMalformedDSN(t *testing.T) {
	_, err := Open("")
	var parseErr *qrdbdriver.DSNParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("Open(\"\") error = %v (%T), want *driver.DSNParseError", err, err)
	}
}

func TestOpen_ReportsAnUnregisteredScheme(t *testing.T) {
	_, err := Open("nosuchscheme://memory?database=app")
	var schemeErr *qrdbdriver.UnsupportedSchemeError
	if !errors.As(err, &schemeErr) {
		t.Fatalf("Open error = %v (%T), want *driver.UnsupportedSchemeError", err, err)
	}
}

func TestOpen_ReturnsTheStockDialectorConfiguredForQRDB(t *testing.T) {
	tests := []string{
		"node://memory?database=app",
		"embedded://memory?database=app",
	}
	for _, dsn := range tests {
		t.Run(dsn, func(t *testing.T) {
			dialector, err := Open(dsn)
			if err != nil {
				t.Fatalf("Open(%q): %v", dsn, err)
			}
			if dialector.Name() != "mysql" {
				t.Errorf("Name() = %q, want the stock dialector's %q", dialector.Name(), "mysql")
			}
			if dialector.DSN != dsn || dialector.DriverName != DriverName {
				t.Errorf("DSN = %q, DriverName = %q; want %q, %q", dialector.DSN, dialector.DriverName, dsn, DriverName)
			}
			if dialector.DefaultStringSize != DefaultStringSize || *dialector.DefaultDatetimePrecision != DefaultDatetimePrecision {
				t.Errorf("defaults = %d, %d; want %d, %d", dialector.DefaultStringSize,
					*dialector.DefaultDatetimePrecision, DefaultStringSize, DefaultDatetimePrecision)
			}
		})
	}
}

// TestOpen_ServerVersionSelectsTheMySQL8Path pins what gorm.io/driver/mysql
// reads from SELECT VERSION() during Initialize. A 5.x or MariaDB version
// string would switch off RENAME COLUMN, RENAME INDEX, FOR SHARE and DATETIME
// precision for every qrdb caller.
func TestOpen_ServerVersionSelectsTheMySQL8Path(t *testing.T) {
	db := openGORM(t, "node://memory?database=version")
	dialector, ok := db.Dialector.(*mysql.Dialector)
	if !ok {
		t.Fatalf("db.Dialector is %T, want *mysql.Dialector", db.Dialector)
	}
	if !strings.HasPrefix(dialector.ServerVersion, "8.") {
		t.Fatalf("ServerVersion = %q, want an 8.x version", dialector.ServerVersion)
	}
	if dialector.DisableDatetimePrecision || dialector.DontSupportRenameColumn ||
		dialector.DontSupportRenameIndex || dialector.DontSupportForShareClause {
		t.Errorf("Initialize took a legacy-server path for %q: %+v", dialector.ServerVersion, *dialector.Config)
	}
}

type widget struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"unique"`
	Note      string
	UpdatedAt time.Time
}

func TestOpen_NodeDSNMigratesWritesAndReads(t *testing.T) {
	db := openGORM(t, "node://memory?database=widgets")
	if err := db.AutoMigrate(&widget{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	if err := db.AutoMigrate(&widget{}); err != nil {
		t.Fatalf("second AutoMigrate: %v", err)
	}
	if err := db.Create(&widget{Name: "sprocket", Note: "n"}).Error; err != nil {
		t.Fatalf("Create: %v", err)
	}

	var got widget
	if err := db.Where("name = ?", "sprocket").First(&got).Error; err != nil {
		t.Fatalf("First: %v", err)
	}
	if got.UpdatedAt.Nanosecond()%int(time.Microsecond) != 0 {
		t.Errorf("UpdatedAt = %v carries sub-microsecond digits; NowFunc must round to %d places",
			got.UpdatedAt, DefaultDatetimePrecision)
	}

	err := db.Create(&widget{Name: "sprocket"}).Error
	if !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Fatalf("duplicate Create = %v (%T), want gorm.ErrDuplicatedKey", err, err)
	}
}

func TestOpen_EmbeddedDSNRefusesDDL(t *testing.T) {
	db := openGORM(t, "embedded://memory?database=kvonly")
	if err := db.AutoMigrate(&widget{}); err == nil {
		t.Fatal("AutoMigrate on embedded:// succeeded; embedded is KV-only and must refuse CREATE TABLE")
	}
}

// TestOpen_CraftedSavePointNameRunsNoSecondStatement pins why a savepoint name
// the stock dialector concatenates into its SQL cannot smuggle in a second
// statement: qrdb's in-process driver refuses multi-statement text.
func TestOpen_CraftedSavePointNameRunsNoSecondStatement(t *testing.T) {
	db := openGORM(t, "node://memory?database=savepoints")
	if err := db.AutoMigrate(&widget{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		return tx.SavePoint("sp1; DROP TABLE widgets").Error
	})
	if err == nil {
		t.Fatal("SavePoint with a second statement in its name succeeded")
	}
	if !db.Migrator().HasTable(&widget{}) {
		t.Fatal("the statement after the savepoint name ran: widgets was dropped")
	}
}

type priced struct {
	ID    uint    `gorm:"primaryKey"`
	Price float64 `gorm:"type:decimal(10,2)"`
}

// recordRawSQL records every statement db issues through the raw callback,
// which is how the migrator issues ALTER TABLE.
func recordRawSQL(t *testing.T, db *gorm.DB) *[]string {
	t.Helper()
	var statements []string
	if err := db.Callback().Raw().After("gorm:raw").Register("qrdbgorm_test:record", func(tx *gorm.DB) {
		statements = append(statements, tx.Statement.SQL.String())
	}); err != nil {
		t.Fatalf("register callback: %v", err)
	}
	return &statements
}

// TestOpen_DecimalColumnReportsLikeMySQL pins the metadata the stock migrator
// compares a decimal field against. These are MySQL 8's values for a
// decimal(10,2) column.
func TestOpen_DecimalColumnReportsLikeMySQL(t *testing.T) {
	db := openGORM(t, "node://memory?database=decimals")
	if err := db.AutoMigrate(&priced{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	var columnType, dataType string
	var precision, scale sql.NullInt64
	var datetimePrecision sql.NullInt64
	if err := db.Raw("SELECT column_type, data_type, numeric_precision, numeric_scale, datetime_precision "+
		"FROM information_schema.columns WHERE table_schema = ? AND table_name = ? AND column_name = ?",
		"decimals", "priceds", "price").Row().Scan(&columnType, &dataType, &precision, &scale, &datetimePrecision); err != nil {
		t.Fatalf("information_schema: %v", err)
	}
	if columnType != "decimal(10,2)" || dataType != "decimal" || precision.Int64 != 10 || scale.Int64 != 2 || datetimePrecision.Valid {
		t.Fatalf("reported %q %q precision=%v scale=%v datetime_precision=%v; want MySQL's decimal(10,2) decimal 10 2 NULL",
			columnType, dataType, precision, scale, datetimePrecision)
	}
}

// TestOpen_DeclaredDecimalTypeMigratesIdempotently shows the documented form
// of a decimal field issues no ALTER on a second AutoMigrate.
func TestOpen_DeclaredDecimalTypeMigratesIdempotently(t *testing.T) {
	db := openGORM(t, "node://memory?database=idempotent")
	if err := db.AutoMigrate(&priced{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	statements := recordRawSQL(t, db)
	if err := db.AutoMigrate(&priced{}); err != nil {
		t.Fatalf("second AutoMigrate: %v", err)
	}
	for _, statement := range *statements {
		if strings.Contains(strings.ToUpper(statement), "ALTER") {
			t.Errorf("second AutoMigrate issued %q", statement)
		}
	}
}
