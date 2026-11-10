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

package quiesce

import (
	"errors"
	"slices"
	"testing"
)

// TestLease_UnregisterWithWorkInFlightStaysInTheDrain: a store that closes
// while an operation it admitted is still running must not take that
// operation out of the drain: the seal waits for it, or names it.
func TestLease_UnregisterWithWorkInFlightStaysInTheDrain(t *testing.T) {
	var g Gate
	l := g.Register("db")
	if err := l.Acquire(); err != nil {
		t.Fatal(err)
	}
	l.Unregister()
	g.Close()

	var timeout *DrainTimeoutError
	if err := g.Drain(-1); !errors.As(err, &timeout) || !slices.Equal(timeout.Stores, []string{"db"}) {
		t.Fatalf("Drain with an unregistered lease in flight = %v; want a DrainTimeoutError naming db", err)
	}

	l.Release()
	if err := g.Drain(-1); err != nil {
		t.Fatalf("Drain after the last operation returned = %v; want nil", err)
	}
	g.mu.Lock()
	n := len(g.leases)
	g.mu.Unlock()
	if n != 0 {
		t.Fatalf("leases = %d after the unregistered lease's last Release; want 0", n)
	}
}

// TestLease_AcquireAfterUnregisterRefuses: a closed store admits nothing,
// even on an open gate, so no operation can start that the drain cannot see.
func TestLease_AcquireAfterUnregisterRefuses(t *testing.T) {
	var g Gate
	l := g.Register("db")
	l.Unregister()
	if err := l.Acquire(); !errors.Is(err, ErrSealing) {
		t.Fatalf("Acquire after Unregister = %v; want ErrSealing", err)
	}
	g.Close()
	if err := g.Drain(-1); err != nil {
		t.Fatalf("Drain after a refused Acquire = %v; want nil (nothing held)", err)
	}
}
