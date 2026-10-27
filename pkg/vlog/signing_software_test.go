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

//go:build qrdb_vlog

package vlog

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// keyReader answers ReadSigningKey from storage, as the service does through
// the group.
type keyReader struct{ stg *memory.MemoryStorage }

func (r keyReader) ReadSigningKey(_ context.Context, id LogID, version uint32) (*SigningKey, error) {
	return ReadSigningKey(r.stg, id, version)
}

// refusingSigner is a HeadSigner whose Sign fails, or whose type is not one
// RFC 9162 allows.
type refusingSigner struct {
	HeadSigner
	typ SignatureType
}

func (s refusingSigner) SignatureType() SignatureType { return s.typ }

func (s refusingSigner) Sign(context.Context, []byte) ([]byte, error) {
	return nil, errors.New("device unavailable")
}

func TestSigningKey_SignsAndVerifiesEachType(t *testing.T) {
	root := bytes.Repeat([]byte{7}, 32)
	for _, st := range signatureTypes {
		t.Run(string(st), func(t *testing.T) {
			s, err := NewSoftwareHeadSigner("acme/certs/issued", mustKey(t, st))
			if err != nil {
				t.Fatalf("NewSoftwareHeadSigner: %v", err)
			}
			if s.SignatureType() != st || s.KeyVersion() != FirstKeyVersion || s.Name() != "acme/certs/issued" {
				t.Fatalf("signer = %s v%d %q", s.SignatureType(), s.KeyVersion(), s.Name())
			}
			if want, _ := KeyHash(s.Name(), s.Public()); s.KeyHash() != want {
				t.Fatalf("KeyHash = %x, want %x", s.KeyHash(), want)
			}
			h, err := SignHead(context.Background(), s, "db.example.com/acme/certs", 9, root)
			if err != nil {
				t.Fatalf("SignHead: %v", err)
			}
			if h.SignatureType != st || h.KeyVersion != FirstKeyVersion || h.Size != 9 {
				t.Fatalf("head = %+v", h)
			}
			note, err := RenderCheckpoint("db.example.com/acme/certs", h, s.Name(), s.KeyHash())
			if err != nil {
				t.Fatalf("RenderCheckpoint: %v", err)
			}
			decoded, err := VerifyCheckpoint(note, "db.example.com/acme/certs", s.Name(), s.Public())
			decoded.KeyVersion = h.KeyVersion
			if err != nil || !reflect.DeepEqual(decoded, h) {
				t.Fatalf("VerifyCheckpoint = %+v, %v; want %+v", decoded, err, h)
			}
			if err := VerifyHead(s.Public(), "db.example.com/acme/certs", decoded); err != nil {
				t.Fatalf("VerifyHead: %v", err)
			}
		})
	}
}

func TestVerifyHead_RefusesTamperedHeads(t *testing.T) {
	const origin = "db.example.com/acme/certs"
	for _, st := range signatureTypes {
		t.Run(string(st), func(t *testing.T) {
			s, err := NewSoftwareHeadSigner("n", mustKey(t, st))
			if err != nil {
				t.Fatalf("NewSoftwareHeadSigner: %v", err)
			}
			h, err := SignHead(context.Background(), s, origin, 3, make([]byte, 32))
			if err != nil {
				t.Fatalf("SignHead: %v", err)
			}
			tamper := map[string]func(SignedHead) (SignedHead, string){
				"size":   func(h SignedHead) (SignedHead, string) { h.Size++; return h, origin },
				"root":   func(h SignedHead) (SignedHead, string) { h.Root = bytes.Repeat([]byte{1}, 32); return h, origin },
				"origin": func(h SignedHead) (SignedHead, string) { return h, origin + "x" },
				"signature": func(h SignedHead) (SignedHead, string) {
					h.Signature = bytes.Repeat([]byte{1}, len(h.Signature))
					return h, origin
				},
			}
			for name, f := range tamper {
				bad, o := f(h)
				var hse *HeadSignatureError
				if err := VerifyHead(s.Public(), o, bad); !errors.As(err, &hse) {
					t.Fatalf("%s tampered: VerifyHead = %v, want *HeadSignatureError", name, err)
				}
			}
		})
	}
}

func TestVerifyHead_RefusesAKeyOfAnotherType(t *testing.T) {
	ed, err := NewSoftwareHeadSigner("n", mustKey(t, SignatureEd25519))
	if err != nil {
		t.Fatalf("NewSoftwareHeadSigner: %v", err)
	}
	p256, err := NewSoftwareHeadSigner("n", mustKey(t, SignatureECDSAP256SHA256))
	if err != nil {
		t.Fatalf("NewSoftwareHeadSigner: %v", err)
	}
	h, err := SignHead(context.Background(), ed, "o", 1, make([]byte, 32))
	if err != nil {
		t.Fatalf("SignHead: %v", err)
	}
	var hse *HeadSignatureError
	if err := VerifyHead(p256.Public(), "o", h); !errors.As(err, &hse) {
		t.Fatalf("VerifyHead with a P-256 key = %v, want *HeadSignatureError", err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	var uke *UnsupportedKeyTypeError
	if err := VerifyHead(&rsaKey.PublicKey, "o", h); !errors.As(err, &uke) {
		t.Fatalf("VerifyHead with an RSA key = %v, want *UnsupportedKeyTypeError", err)
	}
}

func TestNewSoftwareHeadSigner_RefusesOtherKeyTypes(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	for name, key := range map[string]crypto.Signer{"rsa": rsaKey, "p384": p384} {
		t.Run(name, func(t *testing.T) {
			der, err := x509.MarshalPKCS8PrivateKey(key)
			if err != nil {
				t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
			}
			var uke *UnsupportedKeyTypeError
			if _, err := NewSoftwareHeadSigner("n", SigningKey{Version: 1, PKCS8: der}); !errors.As(err, &uke) {
				t.Fatalf("NewSoftwareHeadSigner = %v, want *UnsupportedKeyTypeError", err)
			}
		})
	}
}

func TestChain_CreateWithKey_StagesTheKeyBesideTheLog(t *testing.T) {
	for _, st := range signatureTypes {
		t.Run(string(st), func(t *testing.T) {
			stg := memory.NewStorage()
			key := mustKey(t, st)
			c := NewChain()
			if err := c.CreateWithKey(stg, testLog, keyedDescriptor(t, key), key); err != nil {
				t.Fatalf("CreateWithKey: %v", err)
			}
			got, err := ReadSigningKey(stg, testLog, key.Version)
			if err != nil || !reflect.DeepEqual(*got, key) {
				t.Fatalf("ReadSigningKey = %+v, %v; want %+v", got, err, key)
			}
			desc, err := ReadDescriptor(stg, testLog)
			if want := []HeadKey{headKeyOf(t, key)}; err != nil || !reflect.DeepEqual(desc.Keys, want) {
				t.Fatalf("ReadDescriptor = %+v, %v; want Keys %+v", desc, err, want)
			}
			// The key record is in the log's table: the internal namespace
			// KV Range never returns, and the snapshot keeps.
			k := keyKey(testLog, key.Version)
			if !IsTableKey(k) || !bytes.HasPrefix(k, logPrefix(testLog)) || k[0] != 0 {
				t.Fatalf("key record %q is outside the log's table", k)
			}
			// A restarted chain knows the log and signs with its key.
			restarted := NewChain()
			if err := restarted.Restore(stg); err != nil {
				t.Fatalf("Restore: %v", err)
			}
			var lee *LogExistsError
			if err := restarted.CreateWithKey(stg, testLog, keyedDescriptor(t, key), key); !errors.As(err, &lee) {
				t.Fatalf("second CreateWithKey = %v, want *LogExistsError", err)
			}
			signer, err := NewSoftwareHeadSignerProvider(keyReader{stg}).HeadSigner(context.Background(),
				HeadSignerRequest{TenantID: testLog.TenantID, Database: testLog.Database, Log: testLog.Log, KeyRef: desc.Keys[0].Ref, KeyVersion: desc.Keys[0].Version})
			if err != nil {
				t.Fatalf("HeadSigner: %v", err)
			}
			if signer.SignatureType() != st || signer.Name() != testLog.String() {
				t.Fatalf("signer = %s %q", signer.SignatureType(), signer.Name())
			}
		})
	}
}

func TestSoftwareHeadSignerProvider_Refusals(t *testing.T) {
	p := NewSoftwareHeadSignerProvider(keyReader{memory.NewStorage()})
	req := HeadSignerRequest{TenantID: testLog.TenantID, Database: testLog.Database, Log: testLog.Log}
	var ske *InvalidSigningKeyError
	if _, err := p.HeadSigner(context.Background(), req); !errors.As(err, &ske) {
		t.Fatalf("no KeyRef: %v, want *InvalidSigningKeyError", err)
	}
	req.KeyRef = "2"
	req.KeyVersion = 1
	if _, err := p.HeadSigner(context.Background(), req); !errors.As(err, &ske) {
		t.Fatalf("KeyRef for another version: %v, want *InvalidSigningKeyError", err)
	}
	req.KeyRef = "1"
	var lnf *LogNotFoundError
	if _, err := p.HeadSigner(context.Background(), req); !errors.As(err, &lnf) {
		t.Fatalf("no log: %v, want *LogNotFoundError", err)
	}
}

// TestSoftwareHeadSignerProvider_CreateKeyIsTheSealedRecord checks the
// default provider's key is a k/<version> record the log stores atomically
// with its descriptor, and the heads it signs verify against the descriptor.
func TestSoftwareHeadSignerProvider_CreateKeyIsTheSealedRecord(t *testing.T) {
	for _, st := range signatureTypes {
		t.Run(string(st), func(t *testing.T) {
			stg := memory.NewStorage()
			p := NewSoftwareHeadSignerProvider(keyReader{stg})
			req := CreateKeyRequest{TenantID: testLog.TenantID, Database: testLog.Database, Log: testLog.Log, Type: st, Version: FirstKeyVersion}
			created, err := p.CreateKey(context.Background(), req)
			if err != nil {
				t.Fatalf("CreateKey: %v", err)
			}
			if created.Record == nil || created.Record.Version != FirstKeyVersion || created.Ref != KeyRef(FirstKeyVersion) {
				t.Fatalf("CreateKey = %+v, want a version-1 record referenced %q", created, KeyRef(FirstKeyVersion))
			}
			hk, err := created.HeadKey(req)
			if err != nil {
				t.Fatalf("HeadKey: %v", err)
			}
			desc := testDescriptor(false)
			desc.Keys = []HeadKey{hk}
			b := &batch{}
			if err := NewChain().CreateWithKey(b, testLog, desc, *created.Record); err != nil {
				t.Fatalf("CreateWithKey: %v", err)
			}
			// The descriptor and the key record are in one batch.
			var sawDescriptor, sawKey bool
			for _, op := range b.ops {
				sawDescriptor = sawDescriptor || bytes.Equal(op.key, descriptorKey(testLog))
				sawKey = sawKey || bytes.Equal(op.key, keyKey(testLog, FirstKeyVersion))
			}
			if !sawDescriptor || !sawKey {
				t.Fatalf("create batch: descriptor %v, key record %v; want both", sawDescriptor, sawKey)
			}
			b.commit(t, stg)
			signer, err := p.HeadSigner(context.Background(), HeadSignerRequest{
				TenantID: testLog.TenantID, Database: testLog.Database, Log: testLog.Log, KeyRef: hk.Ref, KeyVersion: hk.Version})
			if err != nil {
				t.Fatalf("HeadSigner: %v", err)
			}
			h, err := SignHead(context.Background(), signer, desc.Origin, 1, make([]byte, 32))
			if err != nil {
				t.Fatalf("SignHead: %v", err)
			}
			if err := desc.VerifyHead(h); err != nil {
				t.Fatalf("Descriptor.VerifyHead: %v", err)
			}
		})
	}
}

func TestSoftwareHeadSignerProvider_CreateKeyRefusals(t *testing.T) {
	p := NewSoftwareHeadSignerProvider(keyReader{memory.NewStorage()})
	var uke *UnsupportedKeyTypeError
	if _, err := p.CreateKey(context.Background(), CreateKeyRequest{Type: "rsa-pss", Version: 1}); !errors.As(err, &uke) {
		t.Fatalf("rsa: CreateKey = %v, want *UnsupportedKeyTypeError", err)
	}
	var ske *InvalidSigningKeyError
	if _, err := p.CreateKey(context.Background(), CreateKeyRequest{Type: SignatureEd25519}); !errors.As(err, &ske) {
		t.Fatalf("version 0: CreateKey = %v, want *InvalidSigningKeyError", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.CreateKey(ctx, CreateKeyRequest{Type: SignatureEd25519, Version: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: CreateKey = %v, want context.Canceled", err)
	}
}

func TestSignHead_Refusals(t *testing.T) {
	s, err := NewSoftwareHeadSigner("n", mustKey(t, SignatureEd25519))
	if err != nil {
		t.Fatalf("NewSoftwareHeadSigner: %v", err)
	}
	var uke *UnsupportedKeyTypeError
	if _, err := SignHead(context.Background(), refusingSigner{s, "rsa-pss"}, "o", 1, nil); !errors.As(err, &uke) {
		t.Fatalf("rsa signer: %v, want *UnsupportedKeyTypeError", err)
	}
	var hse *HeadSignatureError
	if _, err := SignHead(context.Background(), refusingSigner{s, SignatureEd25519}, "o", 1, nil); !errors.As(err, &hse) {
		t.Fatalf("failing signer: %v, want *HeadSignatureError", err)
	}
	// A signer whose signature does not verify with its own public key.
	if _, err := SignHead(context.Background(), refusingSigner{s, SignatureECDSAP256SHA256}, "o", 1, nil); !errors.As(err, &hse) {
		t.Fatalf("lying signer: %v, want *HeadSignatureError", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Sign(ctx, []byte("m")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sign on a canceled context = %v", err)
	}
}

// misreportingSigner signs with its HeadSigner's key and reports another
// key's public half, as a provider bound to the wrong slot would.
type misreportingSigner struct {
	HeadSigner
	public crypto.PublicKey
}

func (s misreportingSigner) Public() crypto.PublicKey { return s.public }

// TestSignHead_RefusesASignatureItsSignerDoesNotVerify: SignHead checks the
// signature against the signer's public key and returns no head it fails.
func TestSignHead_RefusesASignatureItsSignerDoesNotVerify(t *testing.T) {
	s, err := NewSoftwareHeadSigner("db.example.com/events", mustKey(t, SignatureEd25519))
	if err != nil {
		t.Fatalf("NewSoftwareHeadSigner: %v", err)
	}
	other, _, err := mustKey(t, SignatureEd25519).Signer()
	if err != nil {
		t.Fatalf("Signer: %v", err)
	}
	root := bytes.Repeat([]byte{7}, 32)
	var hse *HeadSignatureError
	if _, err := SignHead(context.Background(), misreportingSigner{HeadSigner: s, public: other.Public()}, "db.example.com/events", 1, root); !errors.As(err, &hse) {
		t.Fatalf("SignHead = %v, want *HeadSignatureError", err)
	}
	if _, err := SignHead(context.Background(), s, "db.example.com/events", 1, root); err != nil {
		t.Fatalf("SignHead with a consistent signer = %v", err)
	}
}
