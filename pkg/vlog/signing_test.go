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
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
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

// headKeyOf returns the descriptor's record of the software key k.
func headKeyOf(t *testing.T, k SigningKey) HeadKey {
	t.Helper()
	signer, _, err := k.Signer()
	if err != nil {
		t.Fatalf("Signer: %v", err)
	}
	hk, err := NewHeadKey(k.Version, KeyRef(k.Version), signer.Public())
	if err != nil {
		t.Fatalf("NewHeadKey: %v", err)
	}
	return hk
}

// keyedDescriptor returns a valid descriptor recording the software key k.
func keyedDescriptor(t *testing.T, k SigningKey) Descriptor {
	t.Helper()
	d := testDescriptor(false)
	d.Keys = []HeadKey{headKeyOf(t, k)}
	return d
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

// TestKeyHash_Ed25519IsTheSpecExample checks the Ed25519 key ID against the
// verifier key and signed note c2sp.org/signed-note gives as its example.
func TestKeyHash_Ed25519IsTheSpecExample(t *testing.T) {
	vkey, err := base64.StdEncoding.DecodeString("AekyeRrm56hApGFkyQR4ZCbV54Id2LKaANYcrnKv3U2k")
	if err != nil || len(vkey) != 1+ed25519.PublicKeySize || vkey[0] != 0x01 {
		t.Fatalf("spec vkey = %x, %v", vkey, err)
	}
	pub := ed25519.PublicKey(vkey[1:])
	got, err := KeyHash("example.com/foo", pub)
	if err != nil || got != 0x530d903a {
		t.Fatalf("KeyHash = %08x, %v, want 530d903a", got, err)
	}
	sig, err := base64.StdEncoding.DecodeString("Uw2QOkn8srV1yJGh2VYRlL1Tnagv1YEq6TfXppzi2ONncAlTgK7Ztg1ERYNZXsYjOBH3mFXmRKuwHjG1Yu72IneyaQM=")
	if err != nil || binary.BigEndian.Uint32(sig) != got {
		t.Fatalf("spec signature key ID = %x, %v, want %08x", sig[:4], err, got)
	}
	if !ed25519.Verify(pub, []byte("This is an example message.\n"), sig[4:]) {
		t.Fatal("the spec example signature does not verify under the spec key")
	}
	if other, _ := KeyHash("example.com/bar", pub); other == got {
		t.Fatal("the Ed25519 key ID does not commit to the key name")
	}
}

// TestKeyHash_ECDSAIsTheSPKIHash checks the ECDSA key ID is the truncated
// SHA-256 of the DER SPKI public key alone, as signature type 0x02 assigns.
func TestKeyHash_ECDSAIsTheSPKIHash(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	want := binary.BigEndian.Uint32(sum[:4])
	for _, name := range []string{"example.com/foo", "example.com/bar"} {
		if got, err := KeyHash(name, &key.PublicKey); err != nil || got != want {
			t.Fatalf("KeyHash(%q) = %08x, %v, want %08x", name, got, err, want)
		}
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
	keyed := keyedDescriptor(t, key)
	twoKeys := keyed
	twoKeys.Keys = []HeadKey{keyed.Keys[0], keyed.Keys[0]}
	laterVersion := key
	laterVersion.Version = 2
	cases := map[string]struct {
		desc Descriptor
		key  SigningKey
		want any
	}{
		"rsa key":                 {keyed, SigningKey{Version: 1, PKCS8: rsaDER}, new(*UnsupportedKeyTypeError)},
		"malformed key":           {keyed, SigningKey{Version: 1, PKCS8: []byte("x")}, new(*InvalidSigningKeyError)},
		"record is another key":   {keyedDescriptor(t, mustKey(t, SignatureEd25519)), key, new(*InvalidSigningKeyError)},
		"record at other version": {keyed, laterVersion, new(*InvalidSigningKeyError)},
		"no recorded key":         {testDescriptor(false), key, new(*InvalidDescriptorError)},
		"two recorded keys":       {twoKeys, key, new(*InvalidDescriptorError)},
		"invalid descriptor":      {Descriptor{Keys: keyed.Keys}, key, new(*InvalidDescriptorError)},
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
	key := mustKey(t, SignatureEd25519)
	if err := c.CreateWithKey(w, testLog, keyedDescriptor(t, key), key); !errors.As(err, &twe) {
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
	if err := NewChain().CreateWithKey(stg, testLog, keyedDescriptor(t, key), key); err != nil {
		t.Fatalf("CreateWithKey: %v", err)
	}
	got, err := ReadSnapshot(stg, KeyRequest{ID: testLog, Version: key.Version})
	if err != nil || !reflect.DeepEqual(*got.(*SigningKey), key) {
		t.Fatalf("KeyRequest = %v, %v", got, err)
	}
	d, err := ReadSnapshot(stg, DescriptorRequest{ID: testLog})
	if err != nil || d.(*Descriptor).Origin != testDescriptor(false).Origin {
		t.Fatalf("DescriptorRequest = %v, %v", d, err)
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

// TestNotCompiledError_IsACapabilityRefusal checks the stub's refusal names
// the tag that enables head signing and declares an absent capability.
func TestNotCompiledError_IsACapabilityRefusal(t *testing.T) {
	var err error = &NotCompiledError{}
	if !strings.Contains(err.Error(), "qrdb_vlog") {
		t.Fatalf("Error = %q, want the build tag named", err.Error())
	}
	c, ok := err.(interface{ Capability() string })
	if !ok || c.Capability() != "vlog" {
		t.Fatalf("NotCompiledError is not a capability refusal: %v", ok)
	}
}
