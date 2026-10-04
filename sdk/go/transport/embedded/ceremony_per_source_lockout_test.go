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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRequest_CeremonyInitialize_PerSourceLockoutDoesNotCrossSources is the
// go-qrdb#560 review C acceptance test for the embedded transport. The
// embedded transport has exactly ONE fixed source by construction (there is
// no network peer to distinguish one in-process caller from another), so
// every call through it -- this test's own driver among them -- carries the
// SAME "addr:embedded" principal and necessarily shares one lockout
// counter: that is accurate, not a gap, since an embedder's own process is
// a single trust domain regardless of how many goroutines call through it.
//
// What this test demonstrates is the property that DOES hold: "addr:embedded"
// is its OWN bucket, distinct from any OTHER source reaching the identical
// underlying ceremony/lockout stack. It drives the lockout to engage through
// the embedded transport, then calls the SAME wrapped CeremonyServicer
// DIRECTLY (bypassing the transport) under a context carrying a DIFFERENT
// recorded remote address -- standing in for a different transport, exactly
// as pkg/api/unix's sibling test uses a real TCP gRPC listener for the same
// purpose -- and confirms that caller is unaffected.
func TestRequest_CeremonyInitialize_PerSourceLockoutDoesNotCrossSources(t *testing.T) {
	ctx := context.Background()
	wrapped, ca, _ := servicetest.NewRealReissueStack(t, "correct-horse-battery")
	tr := newExtTransport(t, WithCeremonyService(wrapped), WithCeremonyCA(ca))

	// Drive the embedded transport past the free-failure budget.
	var out *transport.CeremonyInitializeResponse
	for i := 0; i < auth.SOPINFreeFailures; i++ {
		err := tr.Request(ctx, "CeremonyInitialize",
			&transport.CeremonyInitializeRequest{SOPin: "wrong-pin", AdminGenerate: true}, &out)
		if !errors.Is(err, pin.ErrPINInvalid) {
			t.Fatalf("embedded, attempt %d: got %v, want pin.ErrPINInvalid", i+1, err)
		}
	}
	// One more wrong guess: now locked out.
	lockedErr := tr.Request(ctx, "CeremonyInitialize",
		&transport.CeremonyInitializeRequest{SOPin: "wrong-pin", AdminGenerate: true}, &out)
	if !errors.Is(lockedErr, pin.ErrPINLocked) {
		t.Fatalf("embedded after %d wrong guesses: got %v, want pin.ErrPINLocked", auth.SOPINFreeFailures+1, lockedErr)
	}

	// A DIFFERENT source reaching the SAME shared ceremony/lockout stack
	// directly (standing in for a different transport sharing this node's
	// one PIN backend) presents the identical wrong PIN immediately
	// afterward: it must see the ordinary invalid-PIN refusal, never
	// embedded's lockout.
	otherCtx := auth.WithRemoteAddr(context.Background(), "192.168.1.9:6666")
	_, otherErr := wrapped.CeremonyInitialize(otherCtx, ca, &service.CeremonyInitializeOptions{
		SOPin: "wrong-pin", AdminGenerate: true,
	})
	if !errors.Is(otherErr, pin.ErrPINInvalid) {
		t.Fatalf("other source: got %v, want pin.ErrPINInvalid; embedded's lockout leaked to it", otherErr)
	}

	// And the other source can still succeed with the correct PIN while
	// embedded remains locked.
	if _, err := wrapped.CeremonyInitialize(otherCtx, ca, &service.CeremonyInitializeOptions{
		SOPin: "correct-horse-battery", AdminGenerate: true,
	}); err != nil {
		t.Fatalf("other source with the correct PIN: unexpected error: %v", err)
	}
}
