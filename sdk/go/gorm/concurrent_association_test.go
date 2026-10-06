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
	"fmt"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm/clause"

	_ "github.com/jeremyhahn/go-qrdb/sdk/go/driver/node"
)

// ---------------------------------------------------------------------------
// GORM's TestConcurrentMany2ManyAssociation, in pessimistic mode.
//
// The upstream test appends N languages to ONE user concurrently, each
// Append in its own GORM transaction. Every one of those transactions
// rewrites the same user row (GORM saves the owner, touching updated_at)
// and upserts into the join table, so under the default optimistic
// concurrency all but one of them can lose their commit with MySQL 1213 --
// and Append has no retry, so a language goes missing. In pessimistic mode
// the writers of the user row wait for each other instead (docs/row-locking),
// and every append lands.
//
// The models mirror the upstream suite's User/Language many-to-many.
// ---------------------------------------------------------------------------

type concLanguage struct {
	Code string `gorm:"primarykey;size:64"`
	Name string `gorm:"size:64"`
}

type concUser struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	Name      string         `gorm:"size:64"`
	Languages []concLanguage `gorm:"many2many:conc_user_speaks;"`
}

func TestConcurrentMany2ManyAssociation_Pessimistic(t *testing.T) {
	db := openGORM(t, "node://memory?database=concassoc&concurrency=pessimistic")
	if err := db.AutoMigrate(&concLanguage{}, &concUser{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	const count = 6
	languages := make([]concLanguage, count)
	for i := range languages {
		languages[i] = concLanguage{Code: fmt.Sprintf("concurrent %d", i)}
		if err := db.Create(&languages[i]).Error; err != nil {
			t.Fatalf("Create language: %v", err)
		}
	}
	user := concUser{Name: "concurrent"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("Create user: %v", err)
	}
	if err := db.Preload("Languages").FirstOrCreate(&user).Error; err != nil {
		t.Fatalf("FirstOrCreate: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(user concUser, language concLanguage) {
			defer wg.Done()
			errs <- db.Model(&user).Association("Languages").Append(&language)
		}(user, languages[i])
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Append: %v", err)
		}
	}

	var found concUser
	if err := db.Preload(clause.Associations).Where("id = ?", user.ID).First(&found).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := len(found.Languages); got != count {
		t.Fatalf("after concurrent append the user speaks %d languages, want %d", got, count)
	}
}
