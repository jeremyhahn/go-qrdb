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

package keydelivery

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
	"time"
)

func TestReplayGuard_ObserveAndEviction(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	now := base
	g := NewReplayGuard(time.Minute)
	g.now = func() time.Time { return now }

	n1 := bytes.Repeat([]byte{0xA1}, nonceLen)
	n2 := bytes.Repeat([]byte{0xB2}, nonceLen)

	if g.observe(n1) {
		t.Fatal("first observe must be fresh")
	}
	if !g.observe(n1) {
		t.Fatal("second observe of same nonce must be a replay")
	}
	if g.observe(n2) {
		t.Fatal("distinct nonce must be fresh")
	}
	if got := g.Len(); got != 2 {
		t.Fatalf("Len = %d, want 2", got)
	}

	// Advance past retention; both entries should be evicted, and n1 becomes
	// observable again (outside the replay window).
	now = base.Add(2 * time.Minute)
	if got := g.Len(); got != 0 {
		t.Fatalf("Len after expiry = %d, want 0", got)
	}
	if g.observe(n1) {
		t.Fatal("expired nonce must be observable again")
	}
}

func TestNewReplayGuard_DefaultRetention(t *testing.T) {
	g := NewReplayGuard(0)
	if g.retention != DefaultTTL {
		t.Fatalf("retention = %v, want default %v", g.retention, DefaultTTL)
	}
	g2 := NewReplayGuard(-time.Second)
	if g2.retention != DefaultTTL {
		t.Fatalf("negative retention = %v, want default %v", g2.retention, DefaultTTL)
	}
}

func TestReceiver_Options(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	shared := NewReplayGuard(time.Hour)

	r, err := NewReceiver(priv, "node-b", WithTTL(time.Hour), WithReplayGuard(shared))
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	if r.ttl != time.Hour {
		t.Fatalf("ttl = %v, want 1h", r.ttl)
	}
	if r.guard != shared {
		t.Fatal("WithReplayGuard should install the shared guard")
	}

	// Zero/nil option args are ignored (defaults preserved).
	r2, err := NewReceiver(priv, "node-b", WithTTL(0), WithReplayGuard(nil))
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	if r2.ttl != DefaultTTL {
		t.Fatalf("ttl = %v, want default", r2.ttl)
	}
	if r2.guard == nil {
		t.Fatal("nil guard option must leave a default guard")
	}

	// A shared guard rejects a nonce already seen by another receiver.
	env, err := Seal(&priv.PublicKey, goodMeta(), []byte("secret-key-material-bytes-32byte"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	rA := newReceiver(t, priv, "node-b", env.IssuedAt, WithReplayGuard(shared))
	rB := newReceiver(t, priv, "node-b", env.IssuedAt, WithReplayGuard(shared))
	if _, err := rA.Open(env); err != nil {
		t.Fatalf("rA.Open: %v", err)
	}
	if _, err := rB.Open(env); err == nil {
		t.Fatal("shared guard should reject replay across receivers")
	}
}
