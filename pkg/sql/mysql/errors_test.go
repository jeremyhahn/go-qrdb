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

//go:build qrdb_mysql

package mysql

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// ServerStartError
// ---------------------------------------------------------------------------

func TestServerStartError(t *testing.T) {
	cause := errors.New("bind: address already in use")
	err := &ServerStartError{Err: cause}

	assert.Equal(t, "sql/mysql: server start failed: bind: address already in use", err.Error())
	assert.Equal(t, cause, err.Unwrap())
}

func TestServerStartError_NilErr(t *testing.T) {
	err := &ServerStartError{}

	assert.Equal(t, "sql/mysql: server start failed", err.Error())
	assert.Nil(t, err.Unwrap())
}

func TestServerStartError_Unwrap(t *testing.T) {
	cause := errors.New("port in use")
	err := &ServerStartError{Err: cause}

	var target *ServerStartError
	require.True(t, errors.As(err, &target))
	assert.True(t, errors.Is(err, cause))
}

// ---------------------------------------------------------------------------
// AuthenticationError
// ---------------------------------------------------------------------------

func TestAuthenticationError(t *testing.T) {
	cause := errors.New("no peer certificate")
	err := &AuthenticationError{Err: cause}

	assert.Equal(t, "sql/mysql: authentication failed: no peer certificate", err.Error())
	assert.Equal(t, cause, err.Unwrap())
}

func TestAuthenticationError_NilErr(t *testing.T) {
	err := &AuthenticationError{}

	assert.Equal(t, "sql/mysql: authentication failed", err.Error())
	assert.Nil(t, err.Unwrap())
}

func TestAuthenticationError_Unwrap(t *testing.T) {
	cause := errors.New("cert expired")
	err := &AuthenticationError{Err: cause}

	var target *AuthenticationError
	require.True(t, errors.As(err, &target))
	assert.True(t, errors.Is(err, cause))
}

// ---------------------------------------------------------------------------
// Interface compliance
// ---------------------------------------------------------------------------

func TestErrorsImplementErrorInterface(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"ServerStartError", &ServerStartError{Err: errors.New("x")}},
		{"AuthenticationError", &AuthenticationError{Err: errors.New("x")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotEmpty(t, tt.err.Error())
		})
	}
}
