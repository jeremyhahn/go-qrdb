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

package pintest

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
)

func TestStorage_RoundTripAndIsolation(t *testing.T) {
	s := NewStorage()
	ctx := context.Background()
	if _, err := s.Get(ctx, "k"); !errors.Is(err, pin.ErrNotFound) {
		t.Fatalf("Get(missing) = %v, want ErrNotFound", err)
	}
	v := []byte("value")
	if err := s.Put(ctx, "k", v); err != nil {
		t.Fatal(err)
	}
	v[0] = 'X'
	got, err := s.Get(ctx, "k")
	if err != nil || string(got) != "value" {
		t.Fatalf("Get = %q, %v; the store must keep its own copy", got, err)
	}
	got[0] = 'Y'
	if again, _ := s.Get(ctx, "k"); string(again) != "value" {
		t.Fatalf("Get returned an alias of the stored value: %q", again)
	}
}

func TestNewBackend_VerifiesPINs(t *testing.T) {
	b := NewBackend(t)
	if b.SOPINSet() {
		t.Fatal("a fresh backend has no SO PIN")
	}
	if err := b.SetSOPIN("", "so-pin"); err != nil {
		t.Fatal(err)
	}
	if err := b.VerifySOPIN("so-pin"); err != nil {
		t.Fatalf("VerifySOPIN(correct): %v", err)
	}
	if err := b.VerifySOPIN("wrong"); err == nil {
		t.Fatal("VerifySOPIN(wrong) succeeded")
	}
}

// fatalRecorder is a testing.TB whose Fatalf records the failure and stops
// the helper, so a test can assert that a helper fails.
type fatalRecorder struct {
	testing.TB
	failed string
}

// errHelperStopped unwinds a helper that called Fatalf on a fatalRecorder.
var errHelperStopped = errors.New("helper stopped")

func (r *fatalRecorder) Helper() {}

func (r *fatalRecorder) Fatalf(format string, _ ...any) {
	r.failed = format
	panic(errHelperStopped)
}

// runHelper runs fn against a fatalRecorder and reports what it failed with.
func runHelper(t *testing.T, fn func(tb testing.TB)) (failed string) {
	t.Helper()
	r := &fatalRecorder{TB: t}
	defer func() {
		if p := recover(); p != nil {
			if err, ok := p.(error); !ok || !errors.Is(err, errHelperStopped) {
				panic(p)
			}
		}
		failed = r.failed
	}()
	fn(r)
	return ""
}

// TestRequireThrottled: it passes for a backend behind the SO PIN lockout and
// fails for the same raw backend, which admits the right PIN after any number
// of wrong ones, and for a backend with no SO PIN.
func TestRequireThrottled(t *testing.T) {
	if failed := runHelper(t, func(tb testing.TB) { RequireThrottled(tb, pin.NewLockoutBackend(NewSOPINBackend(t))) }); failed != "" {
		t.Fatalf("a throttled backend failed: %s", failed)
	}
	if failed := runHelper(t, func(tb testing.TB) { RequireThrottled(tb, NewSOPINBackend(t)) }); failed == "" {
		t.Fatal("a raw backend passed RequireThrottled")
	}
	if failed := runHelper(t, func(tb testing.TB) { RequireThrottled(tb, NewBackend(t)) }); failed == "" {
		t.Fatal("a backend with no SO PIN passed RequireThrottled")
	}
}
