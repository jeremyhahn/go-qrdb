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
	"crypto/x509"
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

var signatureTypes = []SignatureType{SignatureEd25519, SignatureECDSAP256SHA256}

func mustKey(t *testing.T, st SignatureType) SigningKey {
	t.Helper()
	k, err := GenerateSigningKey(st, FirstKeyVersion)
	if err != nil {
		t.Fatalf("GenerateSigningKey(%s): %v", st, err)
	}
	return k
}

func keyedDescriptor() Descriptor {
	d := testDescriptor(false)
	d.KeyRef = KeyRef(FirstKeyVersion)
	return d
}

// keyReader answers ReadSigningKey from storage, as the service does through
// the group.
type keyReader struct{ stg *memory.MemoryStorage }

func (r keyReader) ReadSigningKey(_ context.Context, id LogID, version uint32) (*SigningKey, error) {
	return ReadSigningKey(r.stg, id, version)
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
			decoded, err := DecodeSignedHead(h.Encode())
			if err != nil || !reflect.DeepEqual(decoded, h) {
				t.Fatalf("DecodeSignedHead = %+v, %v; want %+v", decoded, err, h)
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

func TestSigningKey_RefusesOtherKeyTypes(t *testing.T) {
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
			var uke *UnsupportedKeyTypeError
			if _, err := NewSigningKey(1, key); !errors.As(err, &uke) {
				t.Fatalf("NewSigningKey = %v, want *UnsupportedKeyTypeError", err)
			}
			// The same key arriving as a record is refused when decoded.
			der, err := x509.MarshalPKCS8PrivateKey(key)
			if err != nil {
				t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
			}
			if _, _, err := (SigningKey{Version: 1, PKCS8: der}).Signer(); !errors.As(err, &uke) {
				t.Fatalf("Signer = %v, want *UnsupportedKeyTypeError", err)
			}
			if _, err := NewSoftwareHeadSigner("n", SigningKey{Version: 1, PKCS8: der}); !errors.As(err, &uke) {
				t.Fatalf("NewSoftwareHeadSigner = %v, want *UnsupportedKeyTypeError", err)
			}
		})
	}
	var uke *UnsupportedKeyTypeError
	if _, err := GenerateSigningKey("rsa-pss", 1); !errors.As(err, &uke) {
		t.Fatalf("GenerateSigningKey(rsa-pss) = %v, want *UnsupportedKeyTypeError", err)
	}
	if _, err := KeyHash("n", &rsaKey.PublicKey); !errors.As(err, &uke) {
		t.Fatalf("KeyHash(rsa) = %v, want *UnsupportedKeyTypeError", err)
	}
}

func TestSigningKey_RefusesMalformedKeys(t *testing.T) {
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	var ske *InvalidSigningKeyError
	if _, err := NewSigningKey(0, edKey); !errors.As(err, &ske) {
		t.Fatalf("NewSigningKey(version 0) = %v, want *InvalidSigningKeyError", err)
	}
	for name, k := range map[string]SigningKey{
		"version 0": {PKCS8: mustKey(t, SignatureEd25519).PKCS8},
		"not PKCS8": {Version: 1, PKCS8: []byte("garbage")},
	} {
		if _, _, err := k.Signer(); !errors.As(err, &ske) {
			t.Fatalf("%s: Signer = %v, want *InvalidSigningKeyError", name, err)
		}
	}
}

func TestDecodeSignedHead_Refusals(t *testing.T) {
	var hse *HeadSignatureError
	if _, err := DecodeSignedHead([]byte("{")); !errors.As(err, &hse) {
		t.Fatalf("malformed: %v, want *HeadSignatureError", err)
	}
	var uke *UnsupportedKeyTypeError
	if _, err := DecodeSignedHead(SignedHead{SignatureType: "rsa-pkcs1"}.Encode()); !errors.As(err, &uke) {
		t.Fatalf("rsa type: %v, want *UnsupportedKeyTypeError", err)
	}
}

func TestCheckpointBody_IsTheC2SPForm(t *testing.T) {
	got := string(CheckpointBody("db.example.com/log", 12, []byte{0xff, 0x00}))
	if want := "db.example.com/log\n12\n/wA=\n"; got != want {
		t.Fatalf("CheckpointBody = %q, want %q", got, want)
	}
}

func TestKeyRef_RoundTripsAndRefusesNonVersions(t *testing.T) {
	if v, err := ParseKeyRef(KeyRef(42)); err != nil || v != 42 {
		t.Fatalf("ParseKeyRef(KeyRef(42)) = %d, %v", v, err)
	}
	for _, ref := range []string{"", "0", "-1", "x", "4294967296"} {
		var ske *InvalidSigningKeyError
		if _, err := ParseKeyRef(ref); !errors.As(err, &ske) {
			t.Fatalf("ParseKeyRef(%q) = %v, want *InvalidSigningKeyError", ref, err)
		}
	}
}

func TestChain_CreateWithKey_StagesTheKeyBesideTheLog(t *testing.T) {
	for _, st := range signatureTypes {
		t.Run(string(st), func(t *testing.T) {
			stg := memory.NewStorage()
			key := mustKey(t, st)
			c := NewChain()
			if err := c.CreateWithKey(stg, testLog, keyedDescriptor(), key); err != nil {
				t.Fatalf("CreateWithKey: %v", err)
			}
			got, err := ReadSigningKey(stg, testLog, key.Version)
			if err != nil || !reflect.DeepEqual(*got, key) {
				t.Fatalf("ReadSigningKey = %+v, %v; want %+v", got, err, key)
			}
			desc, err := ReadDescriptor(stg, testLog)
			if err != nil || desc.KeyRef != KeyRef(key.Version) {
				t.Fatalf("ReadDescriptor = %+v, %v; want KeyRef %q", desc, err, KeyRef(key.Version))
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
			if err := restarted.CreateWithKey(stg, testLog, keyedDescriptor(), key); !errors.As(err, &lee) {
				t.Fatalf("second CreateWithKey = %v, want *LogExistsError", err)
			}
			signer, err := NewSoftwareHeadSignerProvider(keyReader{stg}).HeadSigner(context.Background(),
				HeadSignerRequest{TenantID: testLog.TenantID, Database: testLog.Database, Log: testLog.Log, KeyRef: desc.KeyRef})
			if err != nil {
				t.Fatalf("HeadSigner: %v", err)
			}
			if signer.SignatureType() != st || signer.Name() != testLog.String() {
				t.Fatalf("signer = %s %q", signer.SignatureType(), signer.Name())
			}
		})
	}
}

func TestChain_CreateWithKey_Refusals(t *testing.T) {
	key := mustKey(t, SignatureEd25519)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	rsaDER, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	wrongRef := keyedDescriptor()
	wrongRef.KeyRef = "2"
	cases := map[string]struct {
		desc Descriptor
		key  SigningKey
		want any
	}{
		"rsa key":            {keyedDescriptor(), SigningKey{Version: 1, PKCS8: rsaDER}, new(*UnsupportedKeyTypeError)},
		"malformed key":      {keyedDescriptor(), SigningKey{Version: 1, PKCS8: []byte("x")}, new(*InvalidSigningKeyError)},
		"ref names no key":   {wrongRef, key, new(*InvalidDescriptorError)},
		"invalid descriptor": {Descriptor{KeyRef: "1"}, key, new(*InvalidDescriptorError)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stg := memory.NewStorage()
			c := NewChain()
			if err := c.CreateWithKey(stg, testLog, tc.desc, tc.key); !errors.As(err, tc.want) {
				t.Fatalf("CreateWithKey = %v, want %T", err, tc.want)
			}
			if len(dump(t, stg)) != 0 {
				t.Fatal("a refused create wrote records")
			}
		})
	}
}

// A create whose key write fails is one that crashed before its proposal
// committed: the engine drops the batch, and the chain must not know the log.
func TestChain_CreateWithKey_KeyWriteFaultLeavesNeither(t *testing.T) {
	c := NewChain()
	b := &batch{}
	w := failingWriter{Writer: b, failSet: keyKey(testLog, FirstKeyVersion)}
	var twe *TableWriteError
	if err := c.CreateWithKey(w, testLog, keyedDescriptor(), mustKey(t, SignatureEd25519)); !errors.As(err, &twe) {
		t.Fatalf("CreateWithKey = %v, want *TableWriteError", err)
	}
	if _, err := c.AppendEntries(memory.NewStorage(), testLog, entries(0, 1)); err == nil {
		t.Fatal("a log whose create failed accepted an append")
	}
	// Nothing was committed: neither the descriptor nor the key reads back.
	stg := memory.NewStorage()
	var lnf *LogNotFoundError
	if _, err := ReadSigningKey(stg, testLog, FirstKeyVersion); !errors.As(err, &lnf) {
		t.Fatalf("ReadSigningKey = %v, want *LogNotFoundError", err)
	}
	if _, err := ReadDescriptor(stg, testLog); !errors.As(err, &lnf) {
		t.Fatalf("ReadDescriptor = %v, want *LogNotFoundError", err)
	}
}

func TestReadSigningKey_Refusals(t *testing.T) {
	_, stg := mustCreate(t, false)
	var knf *KeyNotFoundError
	if _, err := ReadSigningKey(stg, testLog, 1); !errors.As(err, &knf) || knf.Version != 1 {
		t.Fatalf("keyless log: ReadSigningKey = %v, want *KeyNotFoundError", err)
	}
	var ile *InvalidLogNameError
	if _, err := ReadSigningKey(stg, LogID{TenantID: "t", Database: "d"}, 1); !errors.As(err, &ile) {
		t.Fatalf("invalid id: ReadSigningKey = %v, want *InvalidLogNameError", err)
	}
	if _, err := ReadDescriptor(stg, LogID{TenantID: "t", Database: "d"}); !errors.As(err, &ile) {
		t.Fatalf("invalid id: ReadDescriptor = %v, want *InvalidLogNameError", err)
	}
	var tre *TableReadError
	if _, err := ReadSigningKey(getFailReader{Reader: stg, prefix: keyKey(testLog, 1)}, testLog, 1); !errors.As(err, &tre) {
		t.Fatalf("read fault: ReadSigningKey = %v, want *TableReadError", err)
	}
	if _, err := ReadDescriptor(getFailReader{Reader: stg, prefix: descriptorKey(testLog)}, testLog); !errors.As(err, &tre) {
		t.Fatalf("read fault: ReadDescriptor = %v, want *TableReadError", err)
	}
	if err := stg.Set(descriptorKey(testLog), []byte("{")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	var cte *CorruptTableError
	if _, err := ReadDescriptor(stg, testLog); !errors.As(err, &cte) {
		t.Fatalf("corrupt: ReadDescriptor = %v, want *CorruptTableError", err)
	}
}

func TestRead_AnswersDescriptorAndKeyRequests(t *testing.T) {
	stg := memory.NewStorage()
	key := mustKey(t, SignatureECDSAP256SHA256)
	if err := NewChain().CreateWithKey(stg, testLog, keyedDescriptor(), key); err != nil {
		t.Fatalf("CreateWithKey: %v", err)
	}
	got, err := ReadSnapshot(stg, KeyRequest{ID: testLog, Version: key.Version})
	if err != nil || !reflect.DeepEqual(*got.(*SigningKey), key) {
		t.Fatalf("KeyRequest = %v, %v", got, err)
	}
	d, err := ReadSnapshot(stg, DescriptorRequest{ID: testLog})
	if err != nil || d.(*Descriptor).Origin != keyedDescriptor().Origin {
		t.Fatalf("DescriptorRequest = %v, %v", d, err)
	}
}

func TestSoftwareHeadSignerProvider_Refusals(t *testing.T) {
	p := NewSoftwareHeadSignerProvider(keyReader{memory.NewStorage()})
	req := HeadSignerRequest{TenantID: testLog.TenantID, Database: testLog.Database, Log: testLog.Log}
	var ske *InvalidSigningKeyError
	if _, err := p.HeadSigner(context.Background(), req); !errors.As(err, &ske) {
		t.Fatalf("no KeyRef: %v, want *InvalidSigningKeyError", err)
	}
	req.KeyRef = "1"
	var lnf *LogNotFoundError
	if _, err := p.HeadSigner(context.Background(), req); !errors.As(err, &lnf) {
		t.Fatalf("no log: %v, want *LogNotFoundError", err)
	}
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

func TestErrors_SigningMessages(t *testing.T) {
	for _, err := range []error{
		&UnsupportedKeyTypeError{Type: "rsa"},
		&InvalidSigningKeyError{Reason: "r"},
		&KeyNotFoundError{ID: testLog, Version: 2},
		&HeadSignatureError{Reason: "r"},
	} {
		if err.Error() == "" {
			t.Fatalf("%T has an empty message", err)
		}
	}
}
