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

package barrierinit

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// testNow is the instant every test certificate is valid at.
var testNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// newKey generates a throwaway signing key for one test.
func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return k
}

// thumbprint is the SPKI thumbprint a pending first admin records.
func thumbprint(t *testing.T, pub crypto.PublicKey) string {
	t.Helper()
	tp, err := user.PublicKeySPKIThumbprint(pub)
	if err != nil {
		t.Fatalf("PublicKeySPKIThumbprint: %v", err)
	}
	return tp
}

// testCA is a barrier owner's CA.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newCA(t *testing.T) *testCA {
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "acme CA"},
		NotBefore:             testNow.Add(-time.Hour),
		NotAfter:              testNow.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatalf("CreateCertificate(CA): %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate(CA): %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert: cert, key: key, pool: pool}
}

// issue returns a client certificate for pub signed by the CA.
func (ca *testCA) issue(t *testing.T, pub crypto.PublicKey) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "admin"},
		NotBefore:    testNow.Add(-time.Hour),
		NotAfter:     testNow.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, pub, ca.key)
	if err != nil {
		t.Fatalf("CreateCertificate(admin): %v", err)
	}
	return der
}

// unsigned returns an epoch's passphrase statement for tenant acme's barrier.
func unsigned(epoch uint64, keyRef string) InitStatement {
	return InitStatement{
		Barrier:      clusterseal.TenantBarrier("acme"),
		Epoch:        epoch,
		StrategyID:   seal.StrategyPassphrase,
		KeyRef:       keyRef,
		VerifierSalt: []byte("0123456789abcdef"),
	}
}

// signed signs s with signer, failing the test on error.
func signed(t *testing.T, signer crypto.Signer, s InitStatement) InitStatement {
	t.Helper()
	out, err := Sign(signer, s)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return out
}

// memKV is a faithful in-memory KV: PutIf compares the current value with
// expected the way the state machine's CompareValue/CompareEqual guard does,
// where an absent key holds the empty value. The real store's semantics are
// pinned by the pkg/server adapter test against the standalone engine.
type memKV struct {
	mu sync.Mutex
	m  map[string][]byte
	// err, when set, fails every call.
	err error
}

func newMemKV() *memKV { return &memKV{m: map[string][]byte{}} }

func (k *memKV) Get(_ context.Context, key string) ([]byte, bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.err != nil {
		return nil, false, k.err
	}
	v, ok := k.m[key]
	return bytes.Clone(v), ok, nil
}

func (k *memKV) PutIf(_ context.Context, key string, expected, value []byte) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.err != nil {
		return false, k.err
	}
	if !bytes.Equal(k.m[key], expected) {
		return false, nil
	}
	k.m[key] = bytes.Clone(value)
	return true, nil
}

func (k *memKV) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.err != nil {
		return nil, k.err
	}
	out := map[string][]byte{}
	for key, v := range k.m {
		if strings.HasPrefix(key, prefix) {
			out[key] = bytes.Clone(v)
		}
	}
	return out, nil
}

func (k *memKV) set(key string, value []byte) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = value
}

func (k *memKV) fail(err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.err = err
}
