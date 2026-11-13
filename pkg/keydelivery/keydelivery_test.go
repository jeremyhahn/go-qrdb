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
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func mustP256(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate P-256: %v", err)
	}
	return k
}

// testMode is an arbitrary delivery mode: this package defines none, it only
// binds the named mode into the HPKE context.
const testMode Mode = "test-mode"

func goodMeta() Meta {
	return Meta{
		TenantID:     "acme",
		DatabaseID:   "ledger",
		ShardID:      42,
		ReplicaID:    3,
		TargetNodeID: "node-b",
		Mode:         testMode,
	}
}

// softwareDecrypter converts priv to the Decrypter of its curve's suite: the
// software default a node's own key file yields.
func softwareDecrypter(t *testing.T, priv *ecdsa.PrivateKey) Decrypter {
	t.Helper()
	name, _, err := RecipientFromPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("RecipientFromPublicKey: %v", err)
	}
	dec, err := PrivateKeyFor(name, priv)
	if err != nil {
		t.Fatalf("PrivateKeyFor: %v", err)
	}
	return dec
}

// newReceiver builds a Receiver for priv/nodeID and pins its clock to now.
func newReceiver(t *testing.T, priv *ecdsa.PrivateKey, nodeID string, now time.Time, opts ...ReceiverOption) *Receiver {
	t.Helper()
	r, err := NewReceiver(softwareDecrypter(t, priv), nodeID, opts...)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	r.now = func() time.Time { return now }
	return r
}

func TestSealOpen_RoundTrip(t *testing.T) {
	for _, mode := range []Mode{testMode, "another-mode"} {
		t.Run(string(mode), func(t *testing.T) {
			recip := mustP256(t)
			meta := goodMeta()
			meta.Mode = mode
			secret := []byte("32-byte-sealed-blob-placeholder!")

			env, err := Seal(&recip.PublicKey, meta, secret)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			if len(env.Payload) <= len(secret) {
				t.Fatalf("payload should include encapsulated key, got %d bytes", len(env.Payload))
			}
			if bytes.Contains(env.Payload, secret) {
				t.Fatal("plaintext key must not appear in payload")
			}

			r := newReceiver(t, recip, meta.TargetNodeID, env.IssuedAt)
			got, err := r.Open(env)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !bytes.Equal(got, secret) {
				t.Fatalf("round-trip mismatch: %q", got)
			}
		})
	}
}

func TestSealOpen_JSONWireRoundTrip(t *testing.T) {
	recip := mustP256(t)
	meta := goodMeta()
	secret := []byte("another-32-byte-tenant-root-key!")

	env, err := Seal(&recip.PublicKey, meta, secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Simulate transport over the cluster-peer HTTPS endpoint.
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire Envelope
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	r := newReceiver(t, recip, meta.TargetNodeID, env.IssuedAt)
	got, err := r.Open(&wire)
	if err != nil {
		t.Fatalf("Open after JSON round-trip: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatal("JSON wire round-trip corrupted the key")
	}
}

func TestOpen_WrongRecipientKey(t *testing.T) {
	recip := mustP256(t)
	attacker := mustP256(t)
	meta := goodMeta()

	env, err := Seal(&recip.PublicKey, meta, []byte("secret-key-material-bytes-32byte"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Attacker holds a different private key but the correct NodeID.
	r := newReceiver(t, attacker, meta.TargetNodeID, env.IssuedAt)
	if _, err := r.Open(env); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong recipient key: expected ErrDecrypt, got %v", err)
	}
}

func TestOpen_TamperedContextFailsClosed(t *testing.T) {
	recip := mustP256(t)
	secret := []byte("secret-key-material-bytes-32byte")

	tests := []struct {
		name    string
		tamper  func(*Envelope)
		wantErr error
	}{
		{"tenant", func(e *Envelope) { e.TenantID = "evil" }, ErrDecrypt},
		{"database", func(e *Envelope) { e.DatabaseID = "other" }, ErrDecrypt},
		{"shard", func(e *Envelope) { e.ShardID = 99 }, ErrDecrypt},
		{"replica", func(e *Envelope) { e.ReplicaID = 99 }, ErrDecrypt},
		{"nonce", func(e *Envelope) { e.Nonce[0] ^= 0xFF }, ErrDecrypt},
		{"payload", func(e *Envelope) { e.Payload[len(e.Payload)-1] ^= 0xFF }, ErrDecrypt},
		{"target-node", func(e *Envelope) { e.TargetNodeID = "node-z" }, ErrWrongTarget},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			meta := goodMeta()
			env, err := Seal(&recip.PublicKey, meta, secret)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			tc.tamper(env)
			r := newReceiver(t, recip, meta.TargetNodeID, env.IssuedAt)
			if _, err := r.Open(env); !errors.Is(err, tc.wantErr) {
				t.Fatalf("tamper %s: expected %v, got %v", tc.name, tc.wantErr, err)
			}
		})
	}
}

func TestOpen_TamperedIssuedAtWithinWindow(t *testing.T) {
	recip := mustP256(t)
	meta := goodMeta()
	env, err := Seal(&recip.PublicKey, meta, []byte("secret-key-material-bytes-32byte"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Shift IssuedAt by 1s — still inside the freshness window, so the TTL
	// gate passes, but the HPKE info no longer matches → fails closed.
	r := newReceiver(t, recip, meta.TargetNodeID, env.IssuedAt)
	env.IssuedAt = env.IssuedAt.Add(time.Second)
	if _, err := r.Open(env); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("tampered IssuedAt: expected ErrDecrypt, got %v", err)
	}
}

func TestOpen_Replay(t *testing.T) {
	recip := mustP256(t)
	meta := goodMeta()
	env, err := Seal(&recip.PublicKey, meta, []byte("secret-key-material-bytes-32byte"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	r := newReceiver(t, recip, meta.TargetNodeID, env.IssuedAt)
	if _, err := r.Open(env); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := r.Open(env); !errors.Is(err, ErrReplay) {
		t.Fatalf("replay: expected ErrReplay, got %v", err)
	}
}

func TestOpen_FreshnessWindow(t *testing.T) {
	recip := mustP256(t)
	meta := goodMeta()
	env, err := Seal(&recip.PublicKey, meta, []byte("secret-key-material-bytes-32byte"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	t.Run("expired", func(t *testing.T) {
		r := newReceiver(t, recip, meta.TargetNodeID, env.IssuedAt.Add(DefaultTTL+time.Minute))
		if _, err := r.Open(env); !errors.Is(err, ErrExpired) {
			t.Fatalf("expected ErrExpired, got %v", err)
		}
	})
	t.Run("future-beyond-skew", func(t *testing.T) {
		r := newReceiver(t, recip, meta.TargetNodeID, env.IssuedAt.Add(-2*time.Minute))
		if _, err := r.Open(env); !errors.Is(err, ErrExpired) {
			t.Fatalf("expected ErrExpired, got %v", err)
		}
	})
	t.Run("within-skew", func(t *testing.T) {
		// Issued 10s "in the future" relative to receiver — inside skew.
		r := newReceiver(t, recip, meta.TargetNodeID, env.IssuedAt.Add(-10*time.Second))
		if _, err := r.Open(env); err != nil {
			t.Fatalf("within skew should open: %v", err)
		}
	})
}

func TestSeal_InputValidation(t *testing.T) {
	recip := mustP256(t)
	good := goodMeta()
	secret := []byte("secret-key-material-bytes-32byte")

	if _, err := Seal(nil, good, secret); !errors.Is(err, ErrNilRecipientKey) {
		t.Fatalf("nil key: %v", err)
	}
	if _, err := Seal(&recip.PublicKey, good, nil); !errors.Is(err, ErrEmptyPlaintext) {
		t.Fatalf("empty plaintext: %v", err)
	}
	bad := good
	bad.TenantID = ""
	if _, err := Seal(&recip.PublicKey, bad, secret); !errors.Is(err, ErrInvalidMeta) {
		t.Fatalf("missing tenant: %v", err)
	}
	badMode := good
	badMode.Mode = ""
	if _, err := Seal(&recip.PublicKey, badMode, secret); !errors.Is(err, ErrInvalidMode) {
		t.Fatalf("bad mode: %v", err)
	}
}

func TestMeta_ValidateBranches(t *testing.T) {
	base := goodMeta()
	mut := func(f func(*Meta)) Meta { m := base; f(&m); return m }
	cases := []struct {
		name string
		meta Meta
		want error
	}{
		{"ok", base, nil},
		{"no-tenant", mut(func(m *Meta) { m.TenantID = "" }), ErrInvalidMeta},
		{"no-db", mut(func(m *Meta) { m.DatabaseID = "" }), ErrInvalidMeta},
		{"no-shard", mut(func(m *Meta) { m.ShardID = 0 }), ErrInvalidMeta},
		{"no-replica", mut(func(m *Meta) { m.ReplicaID = 0 }), ErrInvalidMeta},
		{"no-target", mut(func(m *Meta) { m.TargetNodeID = "" }), ErrInvalidMeta},
		{"no-mode", mut(func(m *Meta) { m.Mode = "" }), ErrInvalidMode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.meta.validate()
			if tc.want == nil && err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
		})
	}
}

func TestNewReceiver_Validation(t *testing.T) {
	if _, err := NewReceiver(nil, "node-a"); !errors.Is(err, ErrNilRecipientKey) {
		t.Fatalf("nil decrypter: %v", err)
	}
	if _, err := NewReceiver(softwareDecrypter(t, mustP256(t)), ""); !errors.Is(err, ErrInvalidMeta) {
		t.Fatalf("empty nodeID: %v", err)
	}
	// Non-P-256 (P-384) keys are rejected fail-closed: the DHKEM(P-256) curve
	// is pinned and cannot be downgraded by an unexpected key type.
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("gen P-384: %v", err)
	}
	if _, err := Seal(&p384.PublicKey, goodMeta(), []byte("secret-key-material-bytes-32byte")); !errors.Is(err, ErrUnsupportedKey) {
		t.Fatalf("P-384 Seal: expected ErrUnsupportedKey, got %v", err)
	}
	if _, err := NewReceiver(softwareDecrypter(t, p384), "node-b"); !errors.Is(err, ErrUnsupportedKey) {
		t.Fatalf("P-384 NewReceiver: expected ErrUnsupportedKey, got %v", err)
	}
}

// A node whose key is held on a device opens its deliveries through
// NewDecrypter: the device performs the key agreement, and the scalar is
// never asked for.
func TestReceiver_DeviceHeldKeyOpens(t *testing.T) {
	recip := mustP256(t)
	ek, err := recip.ECDH()
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}
	device := &deviceKX{key: ek}
	dec, err := NewDecrypter(KEMP256, device, nil)
	if err != nil {
		t.Fatalf("NewDecrypter: %v", err)
	}
	if _, err := dec.Bytes(); err == nil {
		t.Fatal("a device-held decrypter must not serialize its private key")
	}

	secret := []byte("32-byte-root-key-or-sealed-blob!")
	env, err := Seal(&recip.PublicKey, goodMeta(), secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	r, err := NewReceiver(dec, goodMeta().TargetNodeID)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	got, err := r.Open(env)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("round-trip mismatch: %q", got)
	}
	if device.ops == 0 {
		t.Fatal("the device performed no key agreement")
	}
}

func TestOpen_MalformedEnvelope(t *testing.T) {
	recip := mustP256(t)
	r := newReceiver(t, recip, "node-b", time.Now())
	good := func() *Envelope {
		e, err := Seal(&recip.PublicKey, goodMeta(), []byte("secret-key-material-bytes-32byte"))
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		return e
	}
	cases := []struct {
		name string
		env  *Envelope
		want error
	}{
		{"nil", nil, ErrMalformedEnvelope},
		{"empty-payload", func() *Envelope { e := good(); e.Payload = nil; return e }(), ErrMalformedEnvelope},
		{"short-nonce", func() *Envelope { e := good(); e.Nonce = e.Nonce[:8]; return e }(), ErrMalformedEnvelope},
		{"zero-issuedat", func() *Envelope { e := good(); e.IssuedAt = time.Time{}; return e }(), ErrMalformedEnvelope},
		{"bad-meta", func() *Envelope { e := good(); e.TenantID = ""; return e }(), ErrInvalidMeta},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.Open(tc.env); !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
		})
	}
}

func TestSealInfo_DistinctPerContext(t *testing.T) {
	nonce := bytes.Repeat([]byte{1}, nonceLen)
	at := time.Unix(1700000000, 0).UTC()
	base := sealInfo(goodMeta(), nonce, at)

	other := goodMeta()
	other.ShardID = 43
	if bytes.Equal(base, sealInfo(other, nonce, at)) {
		t.Fatal("different shard must yield different info")
	}
	if bytes.Equal(base, sealInfo(goodMeta(), nonce, at.Add(time.Nanosecond))) {
		t.Fatal("different IssuedAt must yield different info")
	}
	// Determinism.
	if !bytes.Equal(base, sealInfo(goodMeta(), nonce, at)) {
		t.Fatal("sealInfo must be deterministic")
	}
}
