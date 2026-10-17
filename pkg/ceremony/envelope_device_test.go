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

package ceremony

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// deviceExchanger stands in for a holder's ECDH key held on a device: it
// agrees keys and counts the agreements, and nothing else.
type deviceExchanger struct {
	key *ecdh.PrivateKey
	ops int
}

var _ ecdh.KeyExchanger = (*deviceExchanger)(nil)

func (d *deviceExchanger) PublicKey() *ecdh.PublicKey { return d.key.PublicKey() }
func (d *deviceExchanger) Curve() ecdh.Curve          { return d.key.Curve() }
func (d *deviceExchanger) ECDH(p *ecdh.PublicKey) ([]byte, error) {
	d.ops++
	return d.key.ECDH(p)
}

// sealTestEnvelope seals share to pub under kem as a distribution would.
func sealTestEnvelope(t *testing.T, kem string, pub, share []byte) *ShareEnvelope {
	t.Helper()
	roster := &seal.HolderRoster{Domain: seal.DomainUnseal, Threshold: 2, Total: 3, Distribution: "dist-1"}
	holder := &seal.RosterHolder{Username: "alice", ShareIndex: 1}
	rec := &PersistentOfficer{EncryptionKEM: kem, EncryptionKey: pub}
	raw, err := sealShareEnvelope(ScopeSystem, roster, []byte("roster-digest"), holder, rec, share)
	if err != nil {
		t.Fatalf("sealShareEnvelope: %v", err)
	}
	env, err := ParseShareEnvelope(raw)
	if err != nil {
		t.Fatalf("ParseShareEnvelope: %v", err)
	}
	return env
}

// A holder whose envelope key is held on a device opens the envelope through
// keydelivery.NewDecrypter: the device performs the key agreement and the
// private key is never in this process.
func TestShareEnvelope_OpenWithDeviceHeldKey(t *testing.T) {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	device := &deviceExchanger{key: key}
	share := []byte("c2hhcmUtdmFsdWU=")
	env := sealTestEnvelope(t, keydelivery.KEMP256, key.PublicKey().Bytes(), share)

	dec, err := keydelivery.NewDecrypter(env.KEM, device, nil)
	if err != nil {
		t.Fatalf("NewDecrypter: %v", err)
	}
	if _, err := dec.Bytes(); err == nil {
		t.Fatal("a device-held decrypter must not serialize its private key")
	}
	got, err := env.Open(dec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, share) {
		t.Fatalf("Open = %q, want %q", got, share)
	}
	if device.ops == 0 {
		t.Fatal("the device performed no key agreement")
	}
}

// A device key of another KEM is refused as a typed envelope error rather
// than attempted.
func TestShareEnvelope_OpenWithDeviceKeyOfOtherKEM(t *testing.T) {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	env := sealTestEnvelope(t, keydelivery.KEMP256, key.PublicKey().Bytes(), []byte("c2hhcmU="))

	// P-384 rather than X25519: both differ from the envelope's P-256 KEM,
	// and P-384 is FIPS 140-3 approved, so the mismatch is asserted under
	// GODEBUG=fips140=only too, which refuses X25519 key generation.
	other, err := ecdh.P384().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	dec, err := keydelivery.NewDecrypter(keydelivery.KEMP384, &deviceExchanger{key: other}, nil)
	if err != nil {
		t.Fatalf("NewDecrypter: %v", err)
	}
	_, err = env.Open(dec)
	var envErr *ShareEnvelopeError
	if !errors.As(err, &envErr) {
		t.Fatalf("Open = %v, want *ShareEnvelopeError", err)
	}
	var mismatch *keydelivery.KEMKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Open = %v, want a KEM mismatch", err)
	}
}
