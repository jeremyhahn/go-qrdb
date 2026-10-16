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

package middleware

import (
	"context"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// soGateToleranceBound is how long an SO-gated request may take before the
// tolerance test calls it wedged. A gate decision takes milliseconds; a
// deadlocked one never returns.
const soGateToleranceBound = 30 * time.Second

// pinAddrVerifier is an SOPINVerifier that records the address of the PIN it
// is handed, then delegates to next. With inner set it also allocates, opens
// and destroys protected memory of its own first, as a hardware-backed
// verifier may.
type pinAddrVerifier struct {
	next  SOPINVerifier
	inner *secure.Secret

	mu    sync.Mutex
	addrs []*byte
}

var _ SOPINVerifier = (*pinAddrVerifier)(nil)

func (v *pinAddrVerifier) VerifySOPIN(ctx context.Context, tenantID string, pin []byte) error {
	v.mu.Lock()
	v.addrs = append(v.addrs, unsafe.SliceData(pin)) // #nosec G103 -- address comparison only; nothing is read through it.
	v.mu.Unlock()
	if v.inner != nil {
		scratch, err := secure.NewBuffer(len(pin))
		if err != nil {
			return err
		}
		defer scratch.Destroy()
		opened, err := v.inner.Open(ctx)
		if err != nil {
			return err
		}
		opened.Destroy()
	}
	return v.next.VerifySOPIN(ctx, tenantID, pin)
}

func (v *pinAddrVerifier) pins() []*byte {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]*byte(nil), v.addrs...)
}

// recordOpenedPIN swaps the openSealedPIN seam for one that records the
// address of the buffer each sealed PIN is opened into, restoring it when t
// ends.
func recordOpenedPIN(t *testing.T) func() *byte {
	t.Helper()
	var (
		mu   sync.Mutex
		addr *byte
	)
	prev := openSealedPIN
	openSealedPIN = func(s *secure.Secret, ctx context.Context) (*secure.Buffer, error) {
		buf, err := prev(s, ctx)
		if err != nil {
			return nil, err
		}
		if err := buf.WithBytes(func(pin []byte) error {
			mu.Lock()
			addr = unsafe.SliceData(pin) // #nosec G103 -- address comparison only; nothing is read through it.
			mu.Unlock()
			return nil
		}); err != nil {
			buf.Destroy()
			return nil, err
		}
		return buf, nil
	}
	t.Cleanup(func() { openSealedPIN = prev })
	return func() *byte {
		mu.Lock()
		defer mu.Unlock()
		return addr
	}
}

// The gate hands the verifier the presented PIN's locked pages, not a heap
// copy of them (go-qrdb#145 shape 1): the borrow on the opened PIN is held
// across VerifySOPIN. A transient copy between open and verify changes the
// address the verifier sees and fails here.
func TestSOGate_HandsTheVerifierTheOpenedPIN(t *testing.T) {
	roles, soRoleID := seededSORoles(t)
	verifier := &pinAddrVerifier{next: newSORealFixture(t).verifier}
	gate := NewSOGate(roles, verifier, audit.NewRingBuffer(16), audit.FailClosed, nil)
	opened := recordOpenedPIN(t)

	defer secure.GuardBorrows(t.Fatalf)()
	f := NewSOFactor(t.Context(), []byte(soGatePIN), true, "")
	if err := gate.Enforce(WithSOFactor(officerCtx(soRoleID), f), permission.SealRekey, "r", "a"); err != nil {
		t.Fatalf("Enforce: %v", err)
	}

	want := opened()
	if want == nil {
		t.Fatal("the gate never opened the presented PIN")
	}
	pins := verifier.pins()
	if len(pins) != 1 {
		t.Fatalf("the verifier was called %d times, want once", len(pins))
	}
	if pins[0] != want {
		t.Fatalf("the verifier received %p, not the opened PIN at %p: the PIN crossed "+
			"the heap on its way into the verifier", pins[0], want)
	}
}

// The verifier runs inside the opened PIN's borrow -- the real
// SOAuthenticator itself seals the PIN into a protected buffer of its own
// there -- and a verifier that also allocates, opens and destroys protected
// memory must complete, with the armed guard silent, because none of it
// reenters the PIN's buffer. The request runs under a bounded wait so a
// deadlock fails the test instead of hanging the package.
func TestSOGate_ToleratesAVerifierThatAllocatesProtectedMemory(t *testing.T) {
	inner, err := secure.NewSecret(t.Context(), []byte("the verifier's own secret"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	defer inner.Destroy()

	roles, soRoleID := seededSORoles(t)
	verifier := &pinAddrVerifier{next: newSORealFixture(t).verifier, inner: inner}
	gate := NewSOGate(roles, verifier, audit.NewRingBuffer(16), audit.FailClosed, nil)

	for _, tc := range []struct {
		pin   string
		admit bool
	}{{soGatePIN, true}, {"wrong", false}} {
		f := NewSOFactor(t.Context(), []byte(tc.pin), true, "")
		stop := secure.GuardBorrows(t.Errorf)
		done := make(chan error, 1)
		go func() {
			// A guard violation Goexits this goroutine; the deferred close still
			// unblocks the wait, and stop reports it.
			defer close(done)
			done <- gate.Enforce(WithSOFactor(officerCtx(soRoleID), f), permission.SealRekey, "r", "a")
		}()
		select {
		case err := <-done:
			stop()
			if admitted := err == nil; admitted != tc.admit {
				t.Fatalf("PIN %q: Enforce = %v, want admitted %v", tc.pin, err, tc.admit)
			}
		case <-time.After(soGateToleranceBound):
			stop()
			t.Fatalf("PIN %q: the gate did not decide within %v: a verifier using protected "+
				"memory inside the PIN's borrow wedged it", tc.pin, soGateToleranceBound)
		}
	}
	if got := len(verifier.pins()); got != 2 {
		t.Fatalf("the verifier was called %d times, want twice", got)
	}
}
