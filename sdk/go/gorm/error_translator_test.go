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
	"errors"
	"fmt"
	"testing"

	gmssql "github.com/dolthub/go-mysql-server/sql"
	"gorm.io/gorm"

	qrdbdriver "github.com/jeremyhahn/go-qrdb/sdk/go/driver"
)

func TestTranslate_SQLStateErrorNumberTable(t *testing.T) {
	tests := []struct {
		number uint16
		want   error
	}{
		{1062, gorm.ErrDuplicatedKey},
		{1451, gorm.ErrForeignKeyViolated},
		{1452, gorm.ErrForeignKeyViolated},
		{3819, gorm.ErrCheckConstraintViolated},
	}

	dialector := New(Config{}).(Dialector)
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.number), func(t *testing.T) {
			sqlErr := &qrdbdriver.SQLStateError{Number: tt.number, Message: "boom"}
			if got := dialector.Translate(sqlErr); got != tt.want {
				t.Errorf("Translate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTranslate_SQLStateErrorUnknownNumberPassesThrough(t *testing.T) {
	dialector := New(Config{}).(Dialector)
	sqlErr := &qrdbdriver.SQLStateError{Number: 1146, Message: "unknown table"}

	got := dialector.Translate(sqlErr)
	if got != sqlErr {
		t.Errorf("Translate() = %v, want the original *SQLStateError unchanged", got)
	}
}

func TestTranslate_SQLStateErrorMatchedThroughWrapping(t *testing.T) {
	dialector := New(Config{}).(Dialector)
	sqlErr := &qrdbdriver.SQLStateError{Number: 1062, Message: "dup"}
	wrapped := fmt.Errorf("driver: query execution failed: %w", sqlErr)

	if got := dialector.Translate(wrapped); got != gorm.ErrDuplicatedKey {
		t.Errorf("Translate() = %v, want %v", got, gorm.ErrDuplicatedKey)
	}
}

func TestTranslate_GoMySQLServerKinds(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"primary key violation", gmssql.ErrPrimaryKeyViolation.New(), gorm.ErrDuplicatedKey},
		{"unique key violation", gmssql.ErrUniqueKeyViolation.New(), gorm.ErrDuplicatedKey},
		{"duplicate entry", gmssql.ErrDuplicateEntry.New("PRIMARY"), gorm.ErrDuplicatedKey},
		{"foreign key child violation", gmssql.ErrForeignKeyChildViolation.New("fk1", "child", "parent", "id"), gorm.ErrForeignKeyViolated},
		{"foreign key parent violation", gmssql.ErrForeignKeyParentViolation.New("fk1", "parent", "child", "id"), gorm.ErrForeignKeyViolated},
	}

	dialector := New(Config{}).(Dialector)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dialector.Translate(tt.err); got != tt.want {
				t.Errorf("Translate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTranslate_UnrecognizedErrorPassesThrough(t *testing.T) {
	dialector := New(Config{}).(Dialector)
	original := errors.New("some unrelated failure")

	if got := dialector.Translate(original); got != original {
		t.Errorf("Translate() = %v, want the original error unchanged", got)
	}
}

func TestTranslate_ImplementsErrorTranslator(t *testing.T) {
	if _, ok := New(Config{}).(gorm.ErrorTranslator); !ok {
		t.Fatal("New(Config{}) does not implement gorm.ErrorTranslator")
	}
}
