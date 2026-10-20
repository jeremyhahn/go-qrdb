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

package lifecycle_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// nodeIdentityPEM is a self-signed node certificate and its key, PEM-encoded.
func nodeIdentityPEM(t *testing.T) (der, certPEM, keyPEM []byte, key *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "node-1"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err = x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return der, certPEM, keyPEM, key
}

func TestNodeIdentity_NilIsNoIdentity(t *testing.T) {
	got, err := lifecycle.NodeIdentity(nil)
	if got != nil || err != nil {
		t.Fatalf("NodeIdentity(nil) = %v, %v; want no identity and no error", got, err)
	}
}

func TestNodeIdentity_DeviceHeldCertificate(t *testing.T) {
	der, _, _, key := nodeIdentityPEM(t)
	device := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: &deviceSigner{key: key}}

	got, err := lifecycle.NodeIdentity(&quicraftconfig.MTLSConfig{CACert: []byte("ca"), Certificate: device, Cert: []byte("ignored")})
	if err != nil {
		t.Fatalf("NodeIdentity: %v", err)
	}
	if got != device {
		t.Fatal("NodeIdentity did not return the composer's certificate")
	}
	if _, ok := got.PrivateKey.(*deviceSigner); !ok {
		t.Fatalf("identity key = %T; want the device signer", got.PrivateKey)
	}
}

func TestNodeIdentity_PEMPair(t *testing.T) {
	der, certPEM, keyPEM, _ := nodeIdentityPEM(t)

	got, err := lifecycle.NodeIdentity(&quicraftconfig.MTLSConfig{Cert: certPEM, Key: keyPEM})
	if err != nil {
		t.Fatalf("NodeIdentity: %v", err)
	}
	if len(got.Certificate) != 1 || !bytes.Equal(got.Certificate[0], der) {
		t.Fatal("NodeIdentity did not carry the PEM certificate")
	}
}

func TestNodeIdentity_NotAPairIsTyped(t *testing.T) {
	_, certPEM, _, _ := nodeIdentityPEM(t)
	_, _, otherKeyPEM, _ := nodeIdentityPEM(t)

	for name, id := range map[string]*quicraftconfig.MTLSConfig{
		"no key":        {Cert: certPEM},
		"another's key": {Cert: certPEM, Key: otherKeyPEM},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := lifecycle.NodeIdentity(id)
			var idErr *lifecycle.NodeIdentityError
			if got != nil || !errors.As(err, &idErr) {
				t.Fatalf("NodeIdentity = %v, %v; want *lifecycle.NodeIdentityError", got, err)
			}
		})
	}
}

func TestRemoveClusterCAKeyCache_RemovesTheKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca-key.pem")
	if err := os.WriteFile(keyPath, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(certPath, []byte("cert"), 0o600); err != nil {
		t.Fatal(err)
	}

	lifecycle.RemoveClusterCAKeyCache(dir, slog.New(slog.DiscardHandler))
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("ca-key.pem after the removal: %v; want it gone", err)
	}
	if _, err := os.Stat(certPath); err != nil {
		t.Fatalf("ca.pem after the removal: %v; want it kept", err)
	}
}

func TestRemoveClusterCAKeyCache_NoKeyIsNotAnError(t *testing.T) {
	var logs bytes.Buffer
	lifecycle.RemoveClusterCAKeyCache(t.TempDir(), slog.New(slog.NewTextHandler(&logs, nil)))
	if logs.Len() != 0 {
		t.Fatalf("removing an absent key logged %q; want nothing", logs.String())
	}
	// A nil logger falls back to the default one rather than panicking.
	lifecycle.RemoveClusterCAKeyCache(t.TempDir(), nil)
}

func TestRemoveClusterCAKeyCache_UnremovableKeyIsLogged(t *testing.T) {
	dir := t.TempDir()
	// A non-empty directory under the key's name cannot be removed.
	if err := os.MkdirAll(filepath.Join(dir, "ca-key.pem", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	lifecycle.RemoveClusterCAKeyCache(dir, slog.New(slog.NewTextHandler(&logs, nil)))
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "could not be removed") {
		t.Fatalf("an unremovable key logged %q; want an ERROR", logs.String())
	}
}
