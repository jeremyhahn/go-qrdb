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

package embedded

import (
	"context"
	"testing"
)

// The guarded verbs' semantics are asserted on every transport by
// conformance.TestGuardedVerbConformanceMatrix; this test keeps the
// embedded transport's closed-transport refusal.
func TestEmbedded_GuardedVerbs_ClosedTransport(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx := context.Background()

	if _, err := tr.PutIfAbsent(ctx, "k", []byte("v")); err == nil {
		t.Error("PutIfAbsent on a closed transport should error")
	}
	if _, err := tr.CompareAndDelete(ctx, "k", []byte("v")); err == nil {
		t.Error("CompareAndDelete on a closed transport should error")
	}
}
