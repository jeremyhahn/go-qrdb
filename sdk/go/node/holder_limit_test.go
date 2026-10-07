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

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// TestNode_AnonymousClaimsAreThrottled: the node installs the holder-act
// throttle on its embedded transport, so an in-process caller with no
// identity cannot probe holder usernames without limit. The anonymous policy
// admits a burst of 20 per barrier; the claims below are refused by the
// service (no holder is enrolled), and past the burst by the throttle.
func TestNode_AnonymousClaimsAreThrottled(t *testing.T) {
	ctx := context.Background()
	n := newSharedTenantNode(ctx, t)
	var limited *embedded.HolderActRateLimitedError
	for i := 0; i < 40; i++ {
		_, err := n.Client().Barrier().ShareClaimBegin(ctx, &transport.ShareClaimBeginRequest{Username: "nobody"})
		if errors.As(err, &limited) {
			if i < 20 {
				t.Fatalf("claim %d throttled inside the anonymous burst of 20", i)
			}
			return
		}
	}
	t.Fatal("40 anonymous claims were never throttled")
}

// TestNewHolderActLimit_NodesDoNotShareABucket: two nodes' throttles are
// independent although they share the process-wide engine
// (lifecycle.QuorumLimit).
func TestNewHolderActLimit_NodesDoNotShareABucket(t *testing.T) {
	a, err := newHolderActLimit()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newHolderActLimit()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		a("scope", true)
	}
	if ok, _ := b("scope", true); !ok {
		t.Fatal("exhausting one node's bucket throttled another node")
	}
}
