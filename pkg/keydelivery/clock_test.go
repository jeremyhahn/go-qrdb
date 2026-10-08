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

package keydelivery

import (
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
)

// TestWithClock_DecidesFreshness: the composer's clock, not the wall clock,
// decides whether an envelope is inside its freshness window.
func TestWithClock_DecidesFreshness(t *testing.T) {
	recip := mustP256(t)
	meta := goodMeta()
	env, err := Seal(&recip.PublicKey, meta, []byte("secret-key-material-bytes-32byte"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	fake := clocktest.NewAt(env.IssuedAt)
	r, err := NewReceiver(softwareDecrypter(t, recip), meta.TargetNodeID, WithClock(fake))
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	fake.Advance(DefaultTTL + time.Minute)
	if _, err := r.Open(env); !errors.Is(err, ErrExpired) {
		t.Fatalf("an envelope past the fake clock's window: err = %v, want ErrExpired", err)
	}
}

// TestWithClock_FreshUnderTheFakeClock is the converse: the same envelope opens
// while the fake clock stays inside the window, however much wall time passes.
func TestWithClock_FreshUnderTheFakeClock(t *testing.T) {
	recip := mustP256(t)
	meta := goodMeta()
	env, err := Seal(&recip.PublicKey, meta, []byte("secret-key-material-bytes-32byte"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	r, err := NewReceiver(softwareDecrypter(t, recip), meta.TargetNodeID, WithClock(clocktest.NewAt(env.IssuedAt)))
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	if _, err := r.Open(env); err != nil {
		t.Fatalf("Open under the fake clock: %v", err)
	}
}

// TestWithClock_DefaultGuardFollowsTheClock: the replay guard the Receiver owns
// is driven by the same clock, so a replay is still refused inside the window
// and the nonce is forgotten only once the fake clock moves past it.
func TestWithClock_DefaultGuardFollowsTheClock(t *testing.T) {
	recip := mustP256(t)
	meta := goodMeta()
	env, err := Seal(&recip.PublicKey, meta, []byte("secret-key-material-bytes-32byte"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	fake := clocktest.NewAt(env.IssuedAt)
	r, err := NewReceiver(softwareDecrypter(t, recip), meta.TargetNodeID, WithClock(fake))
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	if _, err := r.Open(env); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	fake.Advance(time.Minute)
	if _, err := r.Open(env); !errors.Is(err, ErrReplay) {
		t.Fatalf("replay inside the window: err = %v, want ErrReplay", err)
	}
}

func TestWithClock_NilKeepsTheWallClock(t *testing.T) {
	recip := mustP256(t)
	meta := goodMeta()
	env, err := Seal(&recip.PublicKey, meta, []byte("secret-key-material-bytes-32byte"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	r, err := NewReceiver(softwareDecrypter(t, recip), meta.TargetNodeID, WithClock(nil))
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	if _, err := r.Open(env); err != nil {
		t.Fatalf("a fresh envelope under the wall clock: %v", err)
	}
}
