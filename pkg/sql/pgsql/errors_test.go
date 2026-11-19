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

//go:build qrdb_pgsql

package pgsql

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServerStartError_WithErr(t *testing.T) {
	cause := errors.New("bind failed")
	err := &ServerStartError{Err: cause}
	assert.Equal(t, "sql/pgsql: server start failed: bind failed", err.Error())
	assert.Equal(t, cause, err.Unwrap())
	assert.True(t, errors.Is(err, cause))
}

func TestServerStartError_NilErr(t *testing.T) {
	err := &ServerStartError{}
	assert.Equal(t, "sql/pgsql: server start failed", err.Error())
	assert.Nil(t, err.Unwrap())
}

func TestAuthenticationError_WithErr(t *testing.T) {
	cause := errors.New("cert expired")
	err := &AuthenticationError{Err: cause}
	assert.Equal(t, "sql/pgsql: authentication failed: cert expired", err.Error())
	assert.Equal(t, cause, err.Unwrap())
	assert.True(t, errors.Is(err, cause))
}

func TestAuthenticationError_NilErr(t *testing.T) {
	err := &AuthenticationError{}
	assert.Equal(t, "sql/pgsql: authentication failed", err.Error())
	assert.Nil(t, err.Unwrap())
}

func TestQueryError_WithErr(t *testing.T) {
	cause := errors.New("syntax error")
	err := &QueryError{Query: "SELECT * FROM kv", Err: cause}
	assert.Equal(t, "sql/pgsql: query failed: SELECT * FROM kv: syntax error", err.Error())
	assert.Equal(t, cause, err.Unwrap())
	assert.True(t, errors.Is(err, cause))
}

func TestQueryError_NilErr(t *testing.T) {
	err := &QueryError{Query: "SELECT 1"}
	assert.Equal(t, "sql/pgsql: query failed: SELECT 1", err.Error())
	assert.Nil(t, err.Unwrap())
}

func TestTranslationError_WithErr(t *testing.T) {
	cause := errors.New("unsupported syntax")
	err := &TranslationError{Query: "SELECT $1::cidr", Err: cause}
	assert.Equal(t, "sql/pgsql: translation failed: SELECT $1::cidr: unsupported syntax", err.Error())
	assert.Equal(t, cause, err.Unwrap())
	assert.True(t, errors.Is(err, cause))
}

func TestTranslationError_NilErr(t *testing.T) {
	err := &TranslationError{Query: "SELECT 1"}
	assert.Equal(t, "sql/pgsql: translation failed: SELECT 1", err.Error())
	assert.Nil(t, err.Unwrap())
}

func TestUnsupportedParameterTypeError_Error(t *testing.T) {
	err := &UnsupportedParameterTypeError{Value: struct{}{}}
	assert.Equal(t, "sql/pgsql: unsupported parameter value type struct {}", err.Error())
}
