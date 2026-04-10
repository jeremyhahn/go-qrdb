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

package shard

import (
	"strings"
	"testing"
)

func TestShardAlreadyRegisteredErrorMessage(t *testing.T) {
	err := &ShardAlreadyRegisteredError{ShardID: 42}
	got := err.Error()
	if !strings.Contains(got, "already registered") {
		t.Errorf("Error() = %q, want it to contain %q", got, "already registered")
	}
	if !strings.Contains(got, "42") {
		t.Errorf("Error() = %q, want it to contain %q", got, "42")
	}
}

func TestShardAlreadyRegisteredErrorWithLargeID(t *testing.T) {
	err := &ShardAlreadyRegisteredError{ShardID: 9999999}
	got := err.Error()
	if !strings.Contains(got, "9999999") {
		t.Errorf("Error() = %q, want it to contain %q", got, "9999999")
	}
}

func TestShardAlreadyRegisteredErrorImplementsError(t *testing.T) {
	err := &ShardAlreadyRegisteredError{ShardID: 1}
	// Verify the concrete type satisfies the error interface.
	var _ error = err
}

func TestShardNotRegisteredErrorMessage(t *testing.T) {
	err := &ShardNotRegisteredError{ShardID: 77}
	got := err.Error()
	if !strings.Contains(got, "not registered") {
		t.Errorf("Error() = %q, want it to contain %q", got, "not registered")
	}
	if !strings.Contains(got, "77") {
		t.Errorf("Error() = %q, want it to contain %q", got, "77")
	}
}

func TestShardNotRegisteredErrorWithZeroID(t *testing.T) {
	err := &ShardNotRegisteredError{ShardID: 0}
	got := err.Error()
	if !strings.Contains(got, "0") {
		t.Errorf("Error() = %q, want it to contain %q", got, "0")
	}
}

func TestShardNotRegisteredErrorImplementsError(t *testing.T) {
	err := &ShardNotRegisteredError{ShardID: 1}
	// Verify the concrete type satisfies the error interface.
	var _ error = err
}

func TestInvalidShardIDErrorMessage(t *testing.T) {
	err := &InvalidShardIDError{ShardID: 0}
	got := err.Error()
	if !strings.Contains(got, "invalid shard ID") {
		t.Errorf("Error() = %q, want it to contain %q", got, "invalid shard ID")
	}
	if !strings.Contains(got, "0") {
		t.Errorf("Error() = %q, want it to contain %q", got, "0")
	}
}

func TestInvalidShardIDErrorWithLargeID(t *testing.T) {
	err := &InvalidShardIDError{ShardID: 18446744073709551615}
	got := err.Error()
	if !strings.Contains(got, "18446744073709551615") {
		t.Errorf("Error() = %q, want it to contain max uint64 value", got)
	}
}

func TestInvalidShardIDErrorImplementsError(t *testing.T) {
	err := &InvalidShardIDError{ShardID: 0}
	// Verify the concrete type satisfies the error interface.
	var _ error = err
}

func TestErrorMessagePrefixes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			"ShardAlreadyRegistered",
			&ShardAlreadyRegisteredError{ShardID: 1},
			"shard: already registered: 1",
		},
		{
			"ShardNotRegistered",
			&ShardNotRegisteredError{ShardID: 2},
			"shard: not registered: 2",
		},
		{
			"InvalidShardID",
			&InvalidShardIDError{ShardID: 3},
			"shard: invalid shard ID: 3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}
