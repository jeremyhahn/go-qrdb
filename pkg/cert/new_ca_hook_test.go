// Copyright (c) 2026 Jeremy Hahn
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

package cert

import (
	"context"
	"sync/atomic"
	"testing"
)

// TestWithNewCAHook_FiresOncePerLoginCA proves the hook fires when the
// provider first comes to hold a tenant's login CA -- the system tenant's at
// Initialize, a new tenant's at its first issuance -- and not again for a CA
// it already holds. That is what lets a client trust pool rebuild exactly
// when the set of trusted CAs grows.
func TestWithNewCAHook_FiresOncePerLoginCA(t *testing.T) {
	var fired atomic.Int32
	p, _, _ := initializeProvider(t, WithNewCAHook(func() { fired.Add(1) }))
	if got := fired.Load(); got != 1 {
		t.Fatalf("after Initialize the hook fired %d times, want 1 (the system tenant's CA)", got)
	}

	ctx := context.Background()
	if _, err := generateForTest(ctx, p, &testRequest{Username: "alice", TenantID: "acme"}); err != nil {
		t.Fatalf("first issuance for acme: %v", err)
	}
	if got := fired.Load(); got != 2 {
		t.Fatalf("after acme's first issuance the hook fired %d times, want 2", got)
	}

	if _, err := generateForTest(ctx, p, &testRequest{Username: "bob", TenantID: "acme"}); err != nil {
		t.Fatalf("second issuance for acme: %v", err)
	}
	if got := fired.Load(); got != 2 {
		t.Fatalf("a second issuance under a CA already held fired the hook (%d, want 2)", got)
	}

	if _, err := generateForTest(ctx, p, &testRequest{Username: "gina", TenantID: "globex"}); err != nil {
		t.Fatalf("first issuance for globex: %v", err)
	}
	if got := fired.Load(); got != 3 {
		t.Fatalf("after globex's first issuance the hook fired %d times, want 3", got)
	}
}

// TestWithNewCAHook_UnsetIsANoOp proves a provider built without the hook
// issues normally.
func TestWithNewCAHook_UnsetIsANoOp(t *testing.T) {
	p, _, _ := initializeProvider(t)
	if _, err := generateForTest(context.Background(), p, &testRequest{Username: "alice", TenantID: "acme"}); err != nil {
		t.Fatalf("issuance without a hook: %v", err)
	}
}
