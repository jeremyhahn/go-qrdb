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

package database

import (
	"context"
	"testing"
	"time"
)

type rollbackCtxKey struct{}

// The rollback context outlives the request's cancellation, keeps its
// values, and is still bounded.
func TestDropRollbackContext(t *testing.T) {
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), rollbackCtxKey{}, "v"))
	cancel()

	ctx, done := DropRollbackContext(parent)
	defer done()

	if err := ctx.Err(); err != nil {
		t.Fatalf("the rollback context inherited the request's cancellation: %v", err)
	}
	if ctx.Value(rollbackCtxKey{}) != "v" {
		t.Fatal("the rollback context lost the request's values")
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > DropRollbackTimeout {
		t.Fatalf("deadline = (%v, %v), want one within DropRollbackTimeout", deadline, ok)
	}
	done()
	if ctx.Err() == nil {
		t.Fatal("cancelling the rollback context must end it")
	}
}
