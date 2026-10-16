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

package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// The PIN borrow must be held across the KDF, not merely across the read that
// fetches the slice header.
//
// This is a LOCK LIFETIME, and a lifetime is not observable from outside the
// window it covers — which is why the three call sites passed pinBuf.Bytes()
// to the KDF for as long as they did, with every success and error test in
// this package still green. Bytes() returns and releases; the hash then runs
// over pages a concurrent Destroy is free to unmap (go-qrdb#74).
//
// The observation is DETERMINISTIC: the buffer itself reports whether its
// borrow callback is running, and the KDF asks at the moment it is called. No
// goroutines, no timing, and the answer is the property itself.
func TestSOAuthenticator_PINBorrowIsHeldAcrossTheKDF(t *testing.T) {
	const tenant = "tenant-1"

	cases := map[string]struct {
		seed    func(t *testing.T, a *SOAuthenticator)
		hookKDF func(t *testing.T, observe func())
		call    func(a *SOAuthenticator) error
	}{
		"SetSOPIN": {
			hookKDF: hookHashPIN,
			call: func(a *SOAuthenticator) error {
				return a.SetSOPIN(context.Background(), tenant, []byte("first-pin"))
			},
		},
		"VerifySOPIN": {
			seed: func(t *testing.T, a *SOAuthenticator) {
				t.Helper()
				if err := a.SetSOPIN(context.Background(), tenant, []byte("first-pin")); err != nil {
					t.Fatalf("seed SetSOPIN: %v", err)
				}
			},
			hookKDF: hookVerifyPIN,
			call: func(a *SOAuthenticator) error {
				return a.VerifySOPIN(context.Background(), tenant, []byte("first-pin"))
			},
		},
		"RotateSOPIN": {
			seed: func(t *testing.T, a *SOAuthenticator) {
				t.Helper()
				if err := a.SetSOPIN(context.Background(), tenant, []byte("first-pin")); err != nil {
					t.Fatalf("seed SetSOPIN: %v", err)
				}
			},
			// Rotate verifies the old PIN and then hashes the new one. The hash
			// is the site measured here, so the hook goes there.
			hookKDF: hookHashPIN,
			call: func(a *SOAuthenticator) error {
				return a.RotateSOPIN(context.Background(), tenant,
					[]byte("first-pin"), []byte("second-pin"))
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := newInternalSOAuth(t, newInternalMemKV())
			if tc.seed != nil {
				tc.seed(t, a)
			}

			// Installed AFTER seeding, so only the measured call is observed.
			observed := installObservingPINBuffer(t)

			var (
				kdfRan     bool
				borrowHeld bool
			)
			tc.hookKDF(t, func() {
				kdfRan = true
				borrowHeld = observed.insideBorrow()
			})

			if err := tc.call(a); err != nil {
				t.Fatalf("%s: %v", name, err)
			}

			if !kdfRan {
				t.Fatal("the KDF was never reached, so nothing was measured")
			}
			if !borrowHeld {
				t.Fatal("the KDF ran OUTSIDE the buffer's borrow: the PIN's pages are " +
					"unmappable by a concurrent Destroy while it is being hashed")
			}
		})
	}
}

// observingPINBuffer wraps a real protected buffer and reports whether its
// borrow callback is currently running.
//
// Wrapping the real buffer rather than replacing it keeps the PIN in protected
// memory for the duration of the test: the observation is added, nothing is
// taken away.
type observingPINBuffer struct {
	inner  pinBuffer
	inside atomic.Bool
}

func (b *observingPINBuffer) WithBytes(fn func([]byte) error) error {
	b.inside.Store(true)
	defer b.inside.Store(false)
	return b.inner.WithBytes(fn)
}

func (b *observingPINBuffer) Destroy() { b.inner.Destroy() }

func (b *observingPINBuffer) insideBorrow() bool { return b.inside.Load() }

// observedBuffers hands back whichever buffer the measured call allocated.
type observedBuffers struct {
	mu   sync.Mutex
	last *observingPINBuffer
}

func (o *observedBuffers) insideBorrow() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.last != nil && o.last.insideBorrow()
}

// installObservingPINBuffer makes the authenticator allocate observing buffers
// for the rest of the test.
func installObservingPINBuffer(t *testing.T) *observedBuffers {
	t.Helper()
	orig := newPINBuffer
	t.Cleanup(func() { newPINBuffer = orig })

	seen := &observedBuffers{}
	newPINBuffer = func(src []byte) (pinBuffer, error) {
		inner, err := orig(src)
		if err != nil {
			return nil, err
		}
		wrapped := &observingPINBuffer{inner: inner}
		seen.mu.Lock()
		seen.last = wrapped
		seen.mu.Unlock()
		return wrapped, nil
	}
	return seen
}

// hookHashPIN observes hashPIN at entry for the duration of one test.
func hookHashPIN(t *testing.T, observe func()) {
	t.Helper()
	orig := hashPIN
	t.Cleanup(func() { hashPIN = orig })
	hashPIN = func(pin []byte) (string, error) {
		observe()
		return orig(pin)
	}
}

// hookVerifyPIN observes verifyPIN at entry for the duration of one test.
func hookVerifyPIN(t *testing.T, observe func()) {
	t.Helper()
	orig := verifyPIN
	t.Cleanup(func() { verifyPIN = orig })
	verifyPIN = func(pin []byte, record string) error {
		observe()
		return orig(pin, record)
	}
}
