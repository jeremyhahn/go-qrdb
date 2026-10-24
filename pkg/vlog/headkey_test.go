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

package vlog

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// deviceSigner is a HeadSigner over a key held outside go-qrdb, the way a
// hardware or KMS provider's signer is: go-qrdb sees only its public half.
type deviceSigner struct {
	name    string
	hash    uint32
	version uint32
	typ     SignatureType
	key     crypto.Signer
}

func (s *deviceSigner) Name() string                 { return s.name }
func (s *deviceSigner) KeyHash() uint32              { return s.hash }
func (s *deviceSigner) KeyVersion() uint32           { return s.version }
func (s *deviceSigner) SignatureType() SignatureType { return s.typ }
func (s *deviceSigner) Public() crypto.PublicKey     { return s.key.Public() }

func (s *deviceSigner) Sign(_ context.Context, msg []byte) ([]byte, error) {
	if s.typ == SignatureEd25519 {
		return s.key.Sign(nil, msg, crypto.Hash(0))
	}
	digest := sha256.Sum256(msg)
	return s.key.Sign(rand.Reader, digest[:], crypto.SHA256)
}

// deviceProvider keeps every key it creates to itself, as a hardware
// provider does: CreateKey returns a reference and a public key and no
// k/<version> record.
type deviceProvider struct {
	keys map[string]crypto.Signer
}

var _ HeadSignerProvider = (*deviceProvider)(nil)

func newDeviceProvider() *deviceProvider {
	return &deviceProvider{keys: make(map[string]crypto.Signer)}
}

func (p *deviceProvider) CreateKey(_ context.Context, req CreateKeyRequest) (CreatedKey, error) {
	var key crypto.Signer
	var err error
	switch req.Type {
	case SignatureEd25519:
		_, key, err = ed25519.GenerateKey(rand.Reader)
	case SignatureECDSAP256SHA256:
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	default:
		return CreatedKey{}, &UnsupportedKeyTypeError{Type: string(req.Type)}
	}
	if err != nil {
		return CreatedKey{}, err
	}
	ref := "device:" + req.Log + "/" + KeyRef(req.Version)
	p.keys[ref] = key
	return CreatedKey{Ref: ref, Public: key.Public()}, nil
}

func (p *deviceProvider) HeadSigner(_ context.Context, req HeadSignerRequest) (HeadSigner, error) {
	id := LogID{TenantID: req.TenantID, Database: req.Database, Log: req.Log}
	key, ok := p.keys[req.KeyRef]
	if !ok {
		return nil, &KeyNotFoundError{ID: id, Version: req.KeyVersion}
	}
	typ, err := publicSignatureType(key.Public())
	if err != nil {
		return nil, err
	}
	hash, err := KeyHash(id.String(), key.Public())
	if err != nil {
		return nil, err
	}
	return &deviceSigner{name: id.String(), hash: hash, version: req.KeyVersion, typ: typ, key: key}, nil
}

// deviceKey creates a key of type st at version through p and returns the
// descriptor's record of it.
func deviceKey(t *testing.T, p *deviceProvider, st SignatureType, version uint32) HeadKey {
	t.Helper()
	req := CreateKeyRequest{TenantID: testLog.TenantID, Database: testLog.Database, Log: testLog.Log, Type: st, Version: version}
	created, err := p.CreateKey(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	hk, err := created.HeadKey(req)
	if err != nil {
		t.Fatalf("HeadKey: %v", err)
	}
	return hk
}

// deviceHead signs a head of size 1 over testLog with key k held by p.
func deviceHead(t *testing.T, p *deviceProvider, origin string, k HeadKey) SignedHead {
	t.Helper()
	signer, err := p.HeadSigner(context.Background(), HeadSignerRequest{
		TenantID: testLog.TenantID, Database: testLog.Database, Log: testLog.Log, KeyRef: k.Ref, KeyVersion: k.Version})
	if err != nil {
		t.Fatalf("HeadSigner: %v", err)
	}
	h, err := SignHead(context.Background(), signer, origin, 1, make([]byte, 32))
	if err != nil {
		t.Fatalf("SignHead: %v", err)
	}
	return h
}

func rsaPublicKey(t *testing.T) *rsa.PublicKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	return &k.PublicKey
}

func TestSignatureType_CheckAcceptsTheRFC9162Types(t *testing.T) {
	for _, st := range signatureTypes {
		if err := st.Check(); err != nil {
			t.Fatalf("%s.Check() = %v", st, err)
		}
	}
}

func TestSignatureType_CheckRefusesOthers(t *testing.T) {
	for _, st := range []SignatureType{"", "rsa-pss", "ecdsa-p384-sha384"} {
		var uke *UnsupportedKeyTypeError
		if err := st.Check(); !errors.As(err, &uke) || uke.Type != string(st) {
			t.Fatalf("%q.Check() = %v, want *UnsupportedKeyTypeError", st, err)
		}
	}
}

func TestNewHeadKey_RecordsThePublicKeyAsPKIX(t *testing.T) {
	for _, st := range signatureTypes {
		t.Run(string(st), func(t *testing.T) {
			signer, _, err := mustKey(t, st).Signer()
			if err != nil {
				t.Fatalf("Signer: %v", err)
			}
			hk, err := NewHeadKey(3, "hsm:slot-1", signer.Public())
			if err != nil {
				t.Fatalf("NewHeadKey: %v", err)
			}
			want, _ := x509.MarshalPKIXPublicKey(signer.Public())
			if hk.Version != 3 || hk.Ref != "hsm:slot-1" || !bytes.Equal(hk.PublicKey, want) {
				t.Fatalf("NewHeadKey = %+v", hk)
			}
			pub, err := hk.Public()
			if err != nil || !reflect.DeepEqual(pub, signer.Public()) {
				t.Fatalf("Public = %v, %v; want the key recorded", pub, err)
			}
		})
	}
}

func TestNewHeadKey_Refusals(t *testing.T) {
	signer, _, err := mustKey(t, SignatureEd25519).Signer()
	if err != nil {
		t.Fatalf("Signer: %v", err)
	}
	var ske *InvalidSigningKeyError
	if _, err := NewHeadKey(0, "r", signer.Public()); !errors.As(err, &ske) {
		t.Fatalf("version 0: NewHeadKey = %v, want *InvalidSigningKeyError", err)
	}
	if _, err := NewHeadKey(1, "", signer.Public()); !errors.As(err, &ske) {
		t.Fatalf("empty ref: NewHeadKey = %v, want *InvalidSigningKeyError", err)
	}
	var uke *UnsupportedKeyTypeError
	if _, err := NewHeadKey(1, "r", rsaPublicKey(t)); !errors.As(err, &uke) {
		t.Fatalf("rsa key: NewHeadKey = %v, want *UnsupportedKeyTypeError", err)
	}
	for name, pub := range map[string]crypto.PublicKey{
		"nil key":           nil,
		"short ed25519 key": ed25519.PublicKey{1, 2, 3},
		"nil ecdsa key":     (*ecdsa.PublicKey)(nil),
		"curveless key":     &ecdsa.PublicKey{},
		"P-256 no point":    &ecdsa.PublicKey{Curve: elliptic.P256()},
		"P-256 off curve":   &ecdsa.PublicKey{Curve: elliptic.P256(), X: big.NewInt(1), Y: big.NewInt(1)},
	} {
		if _, err := NewHeadKey(1, "r", pub); !errors.As(err, &ske) {
			t.Fatalf("%s: NewHeadKey = %v, want *InvalidSigningKeyError", name, err)
		}
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	if _, err := NewHeadKey(1, "r", &p384.PublicKey); !errors.As(err, &uke) {
		t.Fatalf("P-384 key: NewHeadKey = %v, want *UnsupportedKeyTypeError", err)
	}
}

func TestHeadKey_PublicRefusesMalformedAndForeignKeys(t *testing.T) {
	var ske *InvalidSigningKeyError
	if _, err := (HeadKey{Version: 1, Ref: "r", PublicKey: []byte("garbage")}).Public(); !errors.As(err, &ske) {
		t.Fatalf("garbage: Public = %v, want *InvalidSigningKeyError", err)
	}
	der, err := x509.MarshalPKIXPublicKey(rsaPublicKey(t))
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	var uke *UnsupportedKeyTypeError
	if _, err := (HeadKey{Version: 1, Ref: "r", PublicKey: der}).Public(); !errors.As(err, &uke) {
		t.Fatalf("rsa: Public = %v, want *UnsupportedKeyTypeError", err)
	}
}

func TestHeadKey_ValidateAcceptsARecordedKey(t *testing.T) {
	for _, st := range signatureTypes {
		if err := headKeyOf(t, mustKey(t, st)).validate(); err != nil {
			t.Fatalf("%s: validate = %v", st, err)
		}
	}
}

func TestHeadKey_ValidateRefusals(t *testing.T) {
	good := headKeyOf(t, mustKey(t, SignatureEd25519))
	for name, k := range map[string]HeadKey{
		"version 0":   {Ref: good.Ref, PublicKey: good.PublicKey},
		"empty ref":   {Version: 1, PublicKey: good.PublicKey},
		"garbage key": {Version: 1, Ref: good.Ref, PublicKey: []byte("x")},
		"no key":      {Version: 1, Ref: good.Ref},
	} {
		var ske *InvalidSigningKeyError
		if err := k.validate(); !errors.As(err, &ske) {
			t.Fatalf("%s: validate = %v, want *InvalidSigningKeyError", name, err)
		}
	}
}

func TestHeadKey_MatchesItsOwnRecord(t *testing.T) {
	for _, st := range signatureTypes {
		k := mustKey(t, st)
		if err := headKeyOf(t, k).matches(k); err != nil {
			t.Fatalf("%s: matches = %v", st, err)
		}
	}
}

func TestHeadKey_MatchesRefusals(t *testing.T) {
	k := mustKey(t, SignatureEd25519)
	hk := headKeyOf(t, k)
	later := k
	later.Version = 2
	var ske *InvalidSigningKeyError
	for name, record := range map[string]SigningKey{
		"another version": later,
		"another key":     mustKey(t, SignatureEd25519),
		"another type":    mustKey(t, SignatureECDSAP256SHA256),
		"malformed":       {Version: 1, PKCS8: []byte("x")},
	} {
		if err := hk.matches(record); !errors.As(err, &ske) {
			t.Fatalf("%s: matches = %v, want *InvalidSigningKeyError", name, err)
		}
	}
}

func TestCreatedKey_HeadKeyAcceptsAKeyWithoutARecord(t *testing.T) {
	p := newDeviceProvider()
	for _, st := range signatureTypes {
		hk := deviceKey(t, p, st, 2)
		if hk.Version != 2 || !strings.HasPrefix(hk.Ref, "device:") {
			t.Fatalf("%s: HeadKey = %+v", st, hk)
		}
		if _, ok := p.keys[hk.Ref]; !ok {
			t.Fatalf("%s: the recorded ref %q names no device key", st, hk.Ref)
		}
	}
}

func TestCreatedKey_HeadKeyAcceptsAMatchingRecord(t *testing.T) {
	k := mustKey(t, SignatureECDSAP256SHA256)
	signer, _, err := k.Signer()
	if err != nil {
		t.Fatalf("Signer: %v", err)
	}
	req := CreateKeyRequest{Type: SignatureECDSAP256SHA256, Version: FirstKeyVersion}
	hk, err := (CreatedKey{Ref: KeyRef(1), Public: signer.Public(), Record: &k}).HeadKey(req)
	if err != nil || !reflect.DeepEqual(hk, headKeyOf(t, k)) {
		t.Fatalf("HeadKey = %+v, %v; want %+v", hk, err, headKeyOf(t, k))
	}
}

func TestCreatedKey_HeadKeyRefusesAMisbehavingProvider(t *testing.T) {
	k := mustKey(t, SignatureEd25519)
	signer, _, err := k.Signer()
	if err != nil {
		t.Fatalf("Signer: %v", err)
	}
	other := mustKey(t, SignatureEd25519)
	edReq := CreateKeyRequest{Type: SignatureEd25519, Version: FirstKeyVersion}
	cases := map[string]struct {
		key  CreatedKey
		req  CreateKeyRequest
		want any
	}{
		"another type":          {CreatedKey{Ref: "r", Public: signer.Public()}, CreateKeyRequest{Type: SignatureECDSAP256SHA256, Version: 1}, new(*InvalidSigningKeyError)},
		"empty ref":             {CreatedKey{Public: signer.Public()}, edReq, new(*InvalidSigningKeyError)},
		"no public key":         {CreatedKey{Ref: "r"}, edReq, new(*InvalidSigningKeyError)},
		"record of another key": {CreatedKey{Ref: "r", Public: signer.Public(), Record: &other}, edReq, new(*InvalidSigningKeyError)},
		"version 0":             {CreatedKey{Ref: "r", Public: signer.Public()}, CreateKeyRequest{Type: SignatureEd25519}, new(*InvalidSigningKeyError)},
	}
	for name, tc := range cases {
		if _, err := tc.key.HeadKey(tc.req); !errors.As(err, tc.want) {
			t.Fatalf("%s: HeadKey = %v, want %T", name, err, tc.want)
		}
	}
}

func TestDescriptor_KeyAndCurrentKeyOfAKeylessLog(t *testing.T) {
	d := testDescriptor(false)
	if k, ok := d.Key(FirstKeyVersion); ok {
		t.Fatalf("Key(1) = %+v, true; want none", k)
	}
	if k, ok := d.CurrentKey(); ok {
		t.Fatalf("CurrentKey = %+v, true; want none", k)
	}
}

func TestDescriptor_KeyAndCurrentKeyPickByVersion(t *testing.T) {
	p := newDeviceProvider()
	d := testDescriptor(false)
	d.Keys = []HeadKey{deviceKey(t, p, SignatureEd25519, 1), deviceKey(t, p, SignatureECDSAP256SHA256, 2)}
	for v := uint32(1); v <= 2; v++ {
		if k, ok := d.Key(v); !ok || !reflect.DeepEqual(k, d.Keys[v-1]) {
			t.Fatalf("Key(%d) = %+v, %v", v, k, ok)
		}
	}
	for _, v := range []uint32{0, 3} {
		if _, ok := d.Key(v); ok {
			t.Fatalf("Key(%d) found a key", v)
		}
	}
	if k, ok := d.CurrentKey(); !ok || !reflect.DeepEqual(k, d.Keys[1]) {
		t.Fatalf("CurrentKey = %+v, %v; want version 2", k, ok)
	}
}

// TestDescriptor_VerifyHead_KeyHeldOutsideQRDB is the hardware case: a log
// whose key a provider keeps to itself has no k/<version> record, and its
// heads verify against the key its descriptor records, across a rotation.
func TestDescriptor_VerifyHead_KeyHeldOutsideQRDB(t *testing.T) {
	for _, st := range signatureTypes {
		t.Run(string(st), func(t *testing.T) {
			p := newDeviceProvider()
			stg := memory.NewStorage()
			c := NewChain()
			desc := testDescriptor(false)
			desc.Keys = []HeadKey{deviceKey(t, p, st, FirstKeyVersion)}
			if err := c.Create(stg, testLog, desc); err != nil {
				t.Fatalf("Create: %v", err)
			}
			var knf *KeyNotFoundError
			if _, err := ReadSigningKey(stg, testLog, FirstKeyVersion); !errors.As(err, &knf) {
				t.Fatalf("ReadSigningKey = %v, want *KeyNotFoundError: no key record", err)
			}
			first := deviceHead(t, p, desc.Origin, desc.Keys[0])
			if err := desc.VerifyHead(first); err != nil {
				t.Fatalf("VerifyHead(v1): %v", err)
			}
			pub, err := desc.Keys[0].Public()
			if err != nil {
				t.Fatalf("Public: %v", err)
			}
			if err := VerifyHead(pub, desc.Origin, first); err != nil {
				t.Fatalf("vlog.VerifyHead(v1): %v", err)
			}

			next := deviceKey(t, p, st, FirstKeyVersion+1)
			if err := c.RotateKey(stg, stg, testLog, next, nil); err != nil {
				t.Fatalf("RotateKey: %v", err)
			}
			rotated, err := ReadDescriptor(stg, testLog)
			if err != nil {
				t.Fatalf("ReadDescriptor: %v", err)
			}
			second := deviceHead(t, p, desc.Origin, next)
			for name, h := range map[string]SignedHead{"v1": first, "v2": second} {
				if err := rotated.VerifyHead(h); err != nil {
					t.Fatalf("after rotation, VerifyHead(%s): %v", name, err)
				}
			}
			if _, err := ReadSigningKey(stg, testLog, FirstKeyVersion+1); !errors.As(err, &knf) {
				t.Fatalf("ReadSigningKey(v2) = %v, want *KeyNotFoundError: no key record at rotation", err)
			}
		})
	}
}

func TestDescriptor_VerifyHead_Refusals(t *testing.T) {
	p := newDeviceProvider()
	desc := testDescriptor(false)
	desc.Keys = []HeadKey{deviceKey(t, p, SignatureEd25519, FirstKeyVersion)}
	h := deviceHead(t, p, desc.Origin, desc.Keys[0])

	var hse *HeadSignatureError
	unknown := h
	unknown.KeyVersion = 2
	if err := desc.VerifyHead(unknown); !errors.As(err, &hse) {
		t.Fatalf("unrecorded version: VerifyHead = %v, want *HeadSignatureError", err)
	}
	tampered := h
	tampered.Size++
	if err := desc.VerifyHead(tampered); !errors.As(err, &hse) {
		t.Fatalf("tampered: VerifyHead = %v, want *HeadSignatureError", err)
	}
	// A head signed by another key at the recorded version does not verify.
	if err := desc.VerifyHead(deviceHead(t, p, desc.Origin, deviceKey(t, p, SignatureEd25519, FirstKeyVersion))); !errors.As(err, &hse) {
		t.Fatalf("another key: VerifyHead = %v, want *HeadSignatureError", err)
	}
	corrupt := desc
	corrupt.Keys = []HeadKey{{Version: 1, Ref: "r", PublicKey: []byte("x")}}
	var ske *InvalidSigningKeyError
	if err := corrupt.VerifyHead(h); !errors.As(err, &ske) {
		t.Fatalf("corrupt recorded key: VerifyHead = %v, want *InvalidSigningKeyError", err)
	}
}

func TestDescriptor_ValidateRefusesKeysOutOfOrder(t *testing.T) {
	p := newDeviceProvider()
	for name, keys := range map[string][]HeadKey{
		"starts at 2":   {deviceKey(t, p, SignatureEd25519, 2)},
		"skips 2":       {deviceKey(t, p, SignatureEd25519, 1), deviceKey(t, p, SignatureEd25519, 3)},
		"invalid key":   {{Version: 1, Ref: "r", PublicKey: []byte("x")}},
		"repeats one":   {deviceKey(t, p, SignatureEd25519, 1), deviceKey(t, p, SignatureEd25519, 1)},
		"missing a ref": {{Version: 1, PublicKey: deviceKey(t, p, SignatureEd25519, 1).PublicKey}},
	} {
		d := testDescriptor(false)
		d.Keys = keys
		var ide *InvalidDescriptorError
		if err := NewChain().Create(memory.NewStorage(), testLog, d); !errors.As(err, &ide) {
			t.Fatalf("%s: Create = %v, want *InvalidDescriptorError", name, err)
		}
	}
}

func TestChain_RotateKey_AppendsTheSoftwareRecord(t *testing.T) {
	stg := memory.NewStorage()
	c := NewChain()
	first := mustKey(t, SignatureEd25519)
	if err := c.CreateWithKey(stg, testLog, keyedDescriptor(t, first), first); err != nil {
		t.Fatalf("CreateWithKey: %v", err)
	}
	next, err := GenerateSigningKey(SignatureECDSAP256SHA256, FirstKeyVersion+1)
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	if err := c.RotateKey(stg, stg, testLog, headKeyOf(t, next), &next); err != nil {
		t.Fatalf("RotateKey: %v", err)
	}
	desc, err := ReadDescriptor(stg, testLog)
	if want := []HeadKey{headKeyOf(t, first), headKeyOf(t, next)}; err != nil || !reflect.DeepEqual(desc.Keys, want) {
		t.Fatalf("ReadDescriptor = %+v, %v; want Keys %+v", desc, err, want)
	}
	for _, k := range []SigningKey{first, next} {
		got, err := ReadSigningKey(stg, testLog, k.Version)
		if err != nil || !reflect.DeepEqual(*got, k) {
			t.Fatalf("ReadSigningKey(%d) = %+v, %v", k.Version, got, err)
		}
	}
}

// TestChain_RotateKey_ReadsTheStagedDescriptor checks rotations proposed in
// the same apply batch as the create, or as each other, see the descriptor
// the earlier command staged rather than the committed one.
func TestChain_RotateKey_ReadsTheStagedDescriptor(t *testing.T) {
	p := newDeviceProvider()
	stg := memory.NewStorage()
	c := NewChain()
	b := &batch{}
	desc := testDescriptor(false)
	desc.Keys = []HeadKey{deviceKey(t, p, SignatureEd25519, 1)}
	if err := c.Create(b, testLog, desc); err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := desc.Keys
	for v := uint32(2); v <= 3; v++ {
		next := deviceKey(t, p, SignatureEd25519, v)
		if err := c.RotateKey(stg, b, testLog, next, nil); err != nil {
			t.Fatalf("RotateKey(%d) in the create's batch: %v", v, err)
		}
		want = append(want, next)
	}
	b.commit(t, stg)
	got, err := ReadDescriptor(stg, testLog)
	if err != nil || !reflect.DeepEqual(got.Keys, want) {
		t.Fatalf("ReadDescriptor = %+v, %v; want Keys %+v", got, err, want)
	}
}

func TestChain_RotateKey_Refusals(t *testing.T) {
	p := newDeviceProvider()
	first := mustKey(t, SignatureEd25519)
	next, err := GenerateSigningKey(SignatureEd25519, 2)
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	other, err := GenerateSigningKey(SignatureEd25519, 2)
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	cases := map[string]struct {
		id     LogID
		next   HeadKey
		record *SigningKey
		want   any
	}{
		"invalid id":          {LogID{TenantID: "t", Database: "d"}, headKeyOf(t, next), &next, new(*InvalidLogNameError)},
		"invalid key":         {testLog, HeadKey{Version: 2}, nil, new(*InvalidSigningKeyError)},
		"record of other key": {testLog, headKeyOf(t, next), &other, new(*InvalidSigningKeyError)},
		"no such log":         {LogID{TenantID: "acme", Database: "ledger", Log: "absent"}, headKeyOf(t, next), &next, new(*LogNotFoundError)},
		"current version":     {testLog, deviceKey(t, p, SignatureEd25519, 1), nil, new(*KeyVersionConflictError)},
		"skips a version":     {testLog, deviceKey(t, p, SignatureEd25519, 3), nil, new(*KeyVersionConflictError)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stg := memory.NewStorage()
			c := NewChain()
			if err := c.CreateWithKey(stg, testLog, keyedDescriptor(t, first), first); err != nil {
				t.Fatalf("CreateWithKey: %v", err)
			}
			before := dump(t, stg)
			if err := c.RotateKey(stg, stg, tc.id, tc.next, tc.record); !errors.As(err, tc.want) {
				t.Fatalf("RotateKey = %v, want %T", err, tc.want)
			}
			if dump(t, stg) != before {
				t.Fatal("a refused rotation wrote records")
			}
		})
	}
}

func TestChain_RotateKey_WriteFaults(t *testing.T) {
	first := mustKey(t, SignatureEd25519)
	next, err := GenerateSigningKey(SignatureEd25519, 2)
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	for name, failSet := range map[string][]byte{
		"descriptor": descriptorKey(testLog),
		"key record": keyKey(testLog, 2),
	} {
		t.Run(name, func(t *testing.T) {
			stg := memory.NewStorage()
			c := NewChain()
			if err := c.CreateWithKey(stg, testLog, keyedDescriptor(t, first), first); err != nil {
				t.Fatalf("CreateWithKey: %v", err)
			}
			var twe *TableWriteError
			if err := c.RotateKey(stg, failingWriter{Writer: &batch{}, failSet: failSet}, testLog, headKeyOf(t, next), &next); !errors.As(err, &twe) {
				t.Fatalf("RotateKey = %v, want *TableWriteError", err)
			}
		})
	}
}

// TestChain_RotateKey_RefusesAnUnreadableDescriptor: once BeginUpdate drops
// what create staged, RotateKey reads the descriptor from storage, and a
// storage fault refuses the rotation without writing.
func TestChain_RotateKey_RefusesAnUnreadableDescriptor(t *testing.T) {
	first := mustKey(t, SignatureEd25519)
	next, err := GenerateSigningKey(SignatureEd25519, 2)
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	stg := memory.NewStorage()
	c := NewChain()
	if err := c.CreateWithKey(stg, testLog, keyedDescriptor(t, first), first); err != nil {
		t.Fatalf("CreateWithKey: %v", err)
	}
	c.BeginUpdate()
	before := dump(t, stg)
	if err := c.RotateKey(errReader{err: errStorageFault}, stg, testLog, headKeyOf(t, next), &next); !errors.Is(err, errStorageFault) {
		t.Fatalf("RotateKey = %v, want the storage fault", err)
	}
	if dump(t, stg) != before {
		t.Fatal("a refused rotation wrote records")
	}
}

// TestChain_CreateRefusesMoreThanTheFirstKey: a log is created with at most
// its first key; later keys arrive through RotateKey.
func TestChain_CreateRefusesMoreThanTheFirstKey(t *testing.T) {
	first := mustKey(t, SignatureEd25519)
	next, err := GenerateSigningKey(SignatureEd25519, 2)
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	desc := keyedDescriptor(t, first)
	desc.Keys = append(desc.Keys, headKeyOf(t, next))
	stg := memory.NewStorage()
	var ide *InvalidDescriptorError
	if err := NewChain().CreateWithKey(stg, testLog, desc, first); !errors.As(err, &ide) {
		t.Fatalf("CreateWithKey = %v, want *InvalidDescriptorError", err)
	}
	if dump(t, stg) != dump(t, memory.NewStorage()) {
		t.Fatal("a refused create wrote records")
	}
}

func TestKeyVersionConflictError_NamesBothVersions(t *testing.T) {
	msg := (&KeyVersionConflictError{ID: testLog, Version: 3, Want: 2}).Error()
	for _, want := range []string{testLog.String(), "3", "2"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Error() = %q, missing %q", msg, want)
		}
	}
}
