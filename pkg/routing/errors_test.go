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

package routing

import (
	"errors"
	"strings"
	"testing"
)

func TestEmptyShardListError_Error_ReturnsExpectedMessage(t *testing.T) {
	err := &EmptyShardListError{}
	want := "routing: shard list is empty"
	if got := err.Error(); got != want {
		t.Fatalf("EmptyShardListError.Error() = %q, want %q", got, want)
	}
}

func TestEmptyShardListError_Error_ContainsRoutingPrefix(t *testing.T) {
	err := &EmptyShardListError{}
	if !strings.Contains(err.Error(), "routing:") {
		t.Fatalf("EmptyShardListError.Error() = %q, want it to contain %q", err.Error(), "routing:")
	}
}

func TestEmptyShardListError_ImplementsErrorInterface(t *testing.T) {
	var err error = &EmptyShardListError{}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestEmptyShardListError_ErrorsAs(t *testing.T) {
	err := &EmptyShardListError{}
	var target *EmptyShardListError
	if !errors.As(err, &target) {
		t.Fatal("expected errors.As to match *EmptyShardListError")
	}
}
