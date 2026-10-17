// Copyright 2026 Jeremy Hahn
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
	"crypto"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/mlkem"
	"crypto/rand"
	"errors"
	"testing"
)

// deviceKX is an ecdh.KeyExchanger that counts the key agreements it
// performs, standing in for a hardware-held key: the decrypter only ever
// asks it to agree, never for the scalar.
type deviceKX struct {
	key *ecdh.PrivateKey
	ops int
}

func (d *deviceKX) PublicKey() *ecdh.PublicKey { return d.key.PublicKey() }
func (d *deviceKX) Curve() ecdh.Curve          { return d.key.Curve() }
func (d *deviceKX) ECDH(p *ecdh.PublicKey) ([]byte, error) {
	d.ops++
	return d.key.ECDH(p)
}

// devicePQ is a crypto.Decapsulator that counts decapsulations.
type devicePQ struct {
	key *mlkem.DecapsulationKey768
	ops int
}

func (d *devicePQ) Encapsulator() crypto.Encapsulator { return d.key.Encapsulator() }
func (d *devicePQ) Decapsulate(ct []byte) ([]byte, error) {
	d.ops++
	return d.key.Decapsulate(ct)
}

// TestNewDecrypter_DHKEMOpensThroughTheDevice seals to a device-held DHKEM
// key and opens through NewDecrypter: the device performs the agreement.
func TestNewDecrypter_DHKEMOpensThroughTheDevice(t *testing.T) {
	for _, tc := range []struct {
		name  string
		curve ecdh.Curve
	}{{KEMP256, ecdh.P256()}, {KEMP384, ecdh.P384()}, {KEMX25519, ecdh.X25519()}} {
		t.Run(tc.name, func(t *testing.T) {
			if requireRefusedInFIPSMode(t, tc.name) {
				return
			}
			s, err := LookupSuite(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			key, err := tc.curve.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			device := &deviceKX{key: key}
			payload, err := s.Seal(key.PublicKey().Bytes(), []byte("info"), []byte("share"))
			if err != nil {
				t.Fatal(err)
			}
			dec, err := NewDecrypter(tc.name, device, nil)
			if err != nil {
				t.Fatal(err)
			}
			pt, err := s.Open(dec, []byte("info"), payload)
			if err != nil || !bytes.Equal(pt, []byte("share")) {
				t.Fatalf("Open = %q, %v", pt, err)
			}
			if device.ops == 0 {
				t.Fatal("the device was never asked to agree: the key was used in process")
			}
		})
	}
}

// requireRefusedInFIPSMode reports whether FIPS mode is on and suite name is
// not approved, and in that case asserts what a FIPS deployment does with it:
// LookupSuite and NewDecrypter both refuse the name with SuiteNotApprovedError
// before any key is examined. The caller's device round trip is then not
// reachable in this mode -- under GODEBUG=fips140=only crypto/ecdh refuses to
// generate the X25519 key it would need -- so the refusal IS the behaviour
// under test, not a skip.
func requireRefusedInFIPSMode(t *testing.T, name string) bool {
	t.Helper()
	s, ok := suites[name]
	if !ok {
		t.Fatalf("suite %q is not registered", name)
	}
	if s.fipsApproved || !fipsMode() {
		return false
	}
	var notApproved *SuiteNotApprovedError
	if _, err := LookupSuite(name); !errors.As(err, &notApproved) {
		t.Fatalf("LookupSuite(%q) in FIPS mode = %v, want *SuiteNotApprovedError", name, err)
	}
	if _, err := NewDecrypter(name, nil, nil); !errors.As(err, &notApproved) {
		t.Fatalf("NewDecrypter(%q) in FIPS mode = %v, want *SuiteNotApprovedError", name, err)
	}
	return true
}

// TestNewDecrypter_HybridOpensThroughTheDevice does the same for the
// post-quantum hybrid, whose two halves are both device-held.
func TestNewDecrypter_HybridOpensThroughTheDevice(t *testing.T) {
	if requireRefusedInFIPSMode(t, KEMMLKEM768X25519) {
		return
	}
	s, err := LookupSuite(KEMMLKEM768X25519)
	if err != nil {
		t.Fatal(err)
	}
	x, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pq, err := mlkem.GenerateKey768()
	if err != nil {
		t.Fatal(err)
	}
	kx, dpq := &deviceKX{key: x}, &devicePQ{key: pq}
	pub, err := hpke.NewHybridPublicKey(pq.Encapsulator(), x.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := s.Seal(pub.Bytes(), []byte("info"), []byte("share"))
	if err != nil {
		t.Fatal(err)
	}
	dec, err := NewDecrypter(KEMMLKEM768X25519, kx, dpq)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := s.Open(dec, []byte("info"), payload)
	if err != nil || !bytes.Equal(pt, []byte("share")) {
		t.Fatalf("Open = %q, %v", pt, err)
	}
	if kx.ops == 0 || dpq.ops == 0 {
		t.Fatalf("device operations: agreement %d, decapsulation %d", kx.ops, dpq.ops)
	}
}

// TestNewDecrypter_Refusals pins the typed refusals: a missing key, a key of
// another KEM, a stray post-quantum half, an unknown suite, and a hybrid
// without its post-quantum half.
func TestNewDecrypter_Refusals(t *testing.T) {
	p256, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pq, err := mlkem.GenerateKey768()
	if err != nil {
		t.Fatal(err)
	}
	var mismatch *KEMKeyMismatchError
	var unsupported *UnsupportedKEMError
	if _, err := NewDecrypter(KEMP256, nil, nil); !errors.Is(err, ErrNilRecipientKey) {
		t.Fatalf("nil key = %v", err)
	}
	if _, err := NewDecrypter(KEMP384, p256, nil); !errors.As(err, &mismatch) {
		t.Fatalf("a P-256 key under P-384 = %v", err)
	}
	if _, err := NewDecrypter(KEMP256, p256, &devicePQ{key: pq}); !errors.As(err, &mismatch) {
		t.Fatalf("a post-quantum half under a DHKEM = %v", err)
	}
	if _, err := NewDecrypter("nope", p256, nil); !errors.As(err, &unsupported) {
		t.Fatalf("an unknown suite = %v", err)
	}
	// The hybrid's key-shape refusals sit behind its suite refusal, which
	// is what a FIPS deployment meets first.
	if requireRefusedInFIPSMode(t, KEMMLKEM768X25519) {
		return
	}
	x25519, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewDecrypter(KEMMLKEM768X25519, x25519, nil); !errors.As(err, &mismatch) {
		t.Fatalf("a hybrid without its post-quantum half = %v", err)
	}
	if _, err := NewDecrypter(KEMMLKEM768X25519, p256, &devicePQ{key: pq}); !errors.As(err, &mismatch) {
		t.Fatalf("a hybrid over a non-X25519 key = %v", err)
	}
}

// TestRequireRefusedInFIPSMode_AssertsTheRefusal runs the FIPS branch of
// requireRefusedInFIPSMode on an ordinary build, so the assertion the FIPS
// 140-3 job relies on is itself exercised on every run.
func TestRequireRefusedInFIPSMode_AssertsTheRefusal(t *testing.T) {
	withFIPSMode(t, true)
	for _, name := range []string{KEMX25519, KEMMLKEM768X25519} {
		if !requireRefusedInFIPSMode(t, name) {
			t.Fatalf("%s: not treated as refused in FIPS mode", name)
		}
	}
	for _, name := range []string{KEMP256, KEMP384} {
		if requireRefusedInFIPSMode(t, name) {
			t.Fatalf("%s: an approved suite treated as refused in FIPS mode", name)
		}
	}
}

// TestRequireRefusedInFIPSMode_OffOutsideFIPSMode proves the helper leaves
// every suite to its round trip when FIPS mode is off.
func TestRequireRefusedInFIPSMode_OffOutsideFIPSMode(t *testing.T) {
	withFIPSMode(t, false)
	for _, name := range SuiteNames() {
		if requireRefusedInFIPSMode(t, name) {
			t.Fatalf("%s: treated as refused with FIPS mode off", name)
		}
	}
}

// TestPrivateKeyFor_AcceptsADeviceExchanger shows PrivateKeyFor takes any
// ecdh.KeyExchanger, so a device key that also signs opens envelopes sealed
// to its certificate key.
func TestPrivateKeyFor_AcceptsADeviceExchanger(t *testing.T) {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrivateKeyFor(KEMP256, &deviceKX{key: key}); err != nil {
		t.Fatalf("a device exchanger = %v", err)
	}
}
