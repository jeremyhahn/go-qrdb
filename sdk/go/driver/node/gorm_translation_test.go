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
package node

import (
	"errors"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	qrdbgorm "github.com/jeremyhahn/go-qrdb/sdk/go/gorm"
)

// GORM error translation over qrdb's in-process driver, with the STOCK
// gorm.io/driver/mysql dialector. Its translator recognises an engine error
// only when the error IS a go-sql-driver *mysql.MySQLError, so this is the
// end-to-end proof that qrdb's driver hands GORM the same error the MySQL wire
// port does.

type gormCity struct {
	ID   uint   `gorm:"primaryKey"`
	Name string `gorm:"size:64;unique"`
}

type gormCompany struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

type gormEmployee struct {
	ID        uint `gorm:"primaryKey"`
	Name      string
	CompanyID uint
	Company   gormCompany
}

// openStockGORM opens the stock MySQL dialector over a qrdb node:// pool,
// configured the same way qrdbgorm.Open configures it (NewConfig), so this
// test exercises the defaults a real caller gets rather than a hand-rolled
// approximation of them.
func openStockGORM(t *testing.T, database string) *gorm.DB {
	t.Helper()
	cfg := qrdbgorm.NewConfig()
	cfg.Conn = openNode(t, "node://memory?database="+database)
	db, err := gorm.Open(mysql.New(cfg), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return db
}

func TestStockGORM_DuplicateKeyIsErrDuplicatedKey(t *testing.T) {
	db := openStockGORM(t, "gormdup")
	if err := db.AutoMigrate(&gormCity{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	if err := db.Create(&gormCity{Name: "Kabul"}).Error; err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := db.Create(&gormCity{Name: "Kabul"}).Error
	if !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Fatalf("duplicate create = %v (%T), want gorm.ErrDuplicatedKey (MySQL 1062)", err, err)
	}
}

func TestStockGORM_MissingParentIsErrForeignKeyViolated(t *testing.T) {
	db := openStockGORM(t, "gormfk")
	if err := db.AutoMigrate(&gormCompany{}, &gormEmployee{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	err := db.Omit("Company").Create(&gormEmployee{Name: "orphan", CompanyID: 4242}).Error
	if !errors.Is(err, gorm.ErrForeignKeyViolated) {
		t.Fatalf("orphan create = %v (%T), want gorm.ErrForeignKeyViolated (MySQL 1452)", err, err)
	}
}
