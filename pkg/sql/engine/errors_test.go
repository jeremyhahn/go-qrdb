// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package engine

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTranslationError_Error(t *testing.T) {
	err := &TranslationError{
		Query:  "SELECT * FROM t WHERE id::int = 1",
		Reason: "unsupported cast chain",
	}
	expected := "sql/engine: translation failed for query: SELECT * FROM t WHERE id::int = 1: unsupported cast chain"
	assert.Equal(t, expected, err.Error())
}

func TestTranslationError_ImplementsError(t *testing.T) {
	var err error = &TranslationError{Query: "q", Reason: "r"}
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "translation failed")
}

func TestQueryExecutionError_Error(t *testing.T) {
	underlying := errors.New("table not found")
	err := &QueryExecutionError{
		Query: "SELECT * FROM missing",
		Err:   underlying,
	}
	expected := "sql/engine: query execution failed: SELECT * FROM missing: table not found"
	assert.Equal(t, expected, err.Error())
}

func TestQueryExecutionError_Unwrap(t *testing.T) {
	underlying := errors.New("connection refused")
	err := &QueryExecutionError{
		Query: "INSERT INTO t VALUES (1)",
		Err:   underlying,
	}
	assert.True(t, errors.Is(err, underlying))
	assert.Equal(t, underlying, err.Unwrap())
}

func TestQueryExecutionError_ErrorsAs(t *testing.T) {
	underlying := errors.New("timeout")
	err := &QueryExecutionError{Query: "q", Err: underlying}
	var target *QueryExecutionError
	assert.True(t, errors.As(err, &target))
	assert.Equal(t, "q", target.Query)
}

func TestUnsupportedSyntaxError_Error(t *testing.T) {
	err := &UnsupportedSyntaxError{Feature: "LATERAL JOIN"}
	expected := "sql/engine: unsupported PostgreSQL syntax: LATERAL JOIN"
	assert.Equal(t, expected, err.Error())
}

func TestUnsupportedSyntaxError_ImplementsError(t *testing.T) {
	var err error = &UnsupportedSyntaxError{Feature: "ARRAY constructor"}
	assert.NotNil(t, err)
	assert.Contains(t, err.Error(), "unsupported PostgreSQL syntax")
}
