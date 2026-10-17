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

package clusterenroll_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterca"
	pkgclusterenroll "github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

const trustDomain = "device.example.com"

// freePort reserves a TCP port from the shared harness allocator.
func freePort(t *testing.T) int {
	t.Helper()
	port, err := qrdbdharness.ReserveTCPPort()
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	return port
}

// startJoinServer starts the member-side join listener a test joins through.
// It runs in both modes: in FIPS 140-3 mode it holds no Noise key and serves
// the FIPS-approved P-256 suite alone, which this asserts.
func startJoinServer(t *testing.T, provider *cakey.SoftwareProvider, store *kvstoretest.GuardedMemoryStore) *clusterenroll.JoinServer {
	t.Helper()
	srv, err := clusterenroll.StartJoinServer(provider, clusterenroll.JoinServerOptions{
		Host:        "127.0.0.1",
		Port:        freePort(t),
		Store:       store,
		TrustDomain: trustDomain,
	}, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("StartJoinServer: %v", err)
	}
	t.Cleanup(srv.Stop)
	if got, want := srv.PublicKey() != nil, clusterenroll.NoiseAvailable(); got != want {
		t.Fatalf("listener holds a Noise key = %v, want %v (Noise available = %v)", got, want, want)
	}
	if srv.P256PublicKey() == nil {
		t.Fatal("listener holds no P-256 key")
	}
	return srv
}

// joinSuites maps each bootstrap suite a joiner can pin the join listener by
// to the client options that select that suite alone.
var joinSuites = map[string]func(srv *clusterenroll.JoinServer) clusterenroll.JoinClientOptions{
	"noise-nk": func(srv *clusterenroll.JoinServer) clusterenroll.JoinClientOptions {
		return clusterenroll.JoinClientOptions{ServerAddr: srv.Addr(), ServerKey: noiseServerKey(srv)}
	},
	"p256": func(srv *clusterenroll.JoinServer) clusterenroll.JoinClientOptions {
		return clusterenroll.JoinClientOptions{ServerAddr: srv.Addr(), ServerP256Key: srv.P256PublicKey()}
	},
}

// noiseServerKey is the listener's Noise public key. In FIPS 140-3 mode the
// listener holds none, so it is a key of the right size standing for one a
// joiner was handed by a member outside that mode: the client must refuse to
// use it before it dials anything.
func noiseServerKey(srv *clusterenroll.JoinServer) []byte {
	if pub := srv.PublicKey(); pub != nil {
		return pub
	}
	return bytes.Repeat([]byte{0x42}, 32)
}

// noiseRefused reports whether this process must refuse suite: Noise NK is an
// X25519 handshake, which FIPS 140-3 mode does not approve. When it must,
// it asserts the refusal is *NoiseFIPSUnavailableError. The refusal is what
// a FIPS deployment meets, so it is the behaviour under test there, not a
// skip.
func noiseRefused(t *testing.T, suite string, err error) bool {
	t.Helper()
	if suite != "noise-nk" || clusterenroll.NoiseAvailable() {
		return false
	}
	var fipsErr *clusterenroll.NoiseFIPSUnavailableError
	if !errors.As(err, &fipsErr) {
		t.Fatalf("JoinCSR over Noise NK in FIPS 140-3 mode = %v, want *NoiseFIPSUnavailableError", err)
	}
	return true
}

// plainBarrier is a seal.EncryptionBarrier that stores in the clear: the test
// asserts the join path, not the barrier.
type plainBarrier struct{}

func (plainBarrier) Encrypt(p []byte) ([]byte, error) { return p, nil }
func (plainBarrier) Decrypt(c []byte) ([]byte, error) { return c, nil }
func (plainBarrier) IsSealed() bool                   { return false }

// deviceSigner is a crypto.Signer that exposes only the operations a hardware
// key does -- the public key and a signature -- so a request signed through it
// proves the joiner's private key was never needed as bytes.
type deviceSigner struct {
	key   *ecdsa.PrivateKey
	signs int
}

func (d *deviceSigner) Public() crypto.PublicKey { return d.key.Public() }

func (d *deviceSigner) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	d.signs++
	return d.key.Sign(r, digest, opts)
}

// TestJoinCSR_DeviceHeldKeyJoinsThroughTheSDK: a joiner whose key is behind a
// crypto.Signer is issued a cluster leaf through the SDK surface, and the leaf
// certifies the device's public key. It runs over each bootstrap suite: P-256
// joins in both modes, and Noise NK is refused in FIPS 140-3 mode.
func TestJoinCSR_DeviceHeldKeyJoinsThroughTheSDK(t *testing.T) {
	for suite, options := range joinSuites {
		t.Run(suite, func(t *testing.T) {
			t.Cleanup(pkgclusterenroll.ResetJoinAttemptGuardForTest())

			ctx := context.Background()
			store := kvstoretest.NewGuardedMemoryStore()
			provider := clusterca.NewProvider(store, plainBarrier{})
			if err := provider.Initialize(ctx); err != nil {
				t.Fatalf("Initialize cluster CA: %v", err)
			}
			srv := startJoinServer(t, provider, store)

			nodeURI := clusterenroll.NodeURI(trustDomain, "node-7")
			token, _, err := clusterenroll.MintJoinToken(ctx, store, trustDomain, "node-7", time.Hour)
			if err != nil {
				t.Fatalf("MintJoinToken: %v", err)
			}

			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			device := &deviceSigner{key: key}
			uri, err := url.Parse(nodeURI)
			if err != nil {
				t.Fatal(err)
			}
			csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
				Subject: pkix.Name{CommonName: "qrdb-node"},
				URIs:    []*url.URL{uri},
			}, device)
			if err != nil {
				t.Fatalf("CreateCertificateRequest: %v", err)
			}
			if device.signs == 0 {
				t.Fatal("the request was not signed through the device")
			}

			opts := options(srv)
			opts.Token = token
			leafPEM, err := clusterenroll.JoinCSR(ctx, &opts, csrDER, nodeURI)
			if noiseRefused(t, suite, err) {
				return
			}
			if err != nil {
				t.Fatalf("JoinCSR: %v", err)
			}
			block, _ := pem.Decode(leafPEM)
			if block == nil {
				t.Fatal("JoinCSR returned no PEM certificate")
			}
			leaf, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatalf("ParseCertificate: %v", err)
			}
			if got, ok := leaf.PublicKey.(*ecdsa.PublicKey); !ok || !got.Equal(&key.PublicKey) {
				t.Fatal("the issued leaf does not certify the device's public key")
			}
		})
	}
}

func TestJoinCSR_RefusedTokenIsAJoinRefusedError(t *testing.T) {
	for suite, options := range joinSuites {
		t.Run(suite, func(t *testing.T) {
			t.Cleanup(pkgclusterenroll.ResetJoinAttemptGuardForTest())

			ctx := context.Background()
			store := kvstoretest.NewGuardedMemoryStore()
			provider := clusterca.NewProvider(store, plainBarrier{})
			if err := provider.Initialize(ctx); err != nil {
				t.Fatalf("Initialize cluster CA: %v", err)
			}
			srv := startJoinServer(t, provider, store)

			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
			if err != nil {
				t.Fatal(err)
			}
			opts := options(srv)
			opts.Token = "not-a-minted-token"
			_, err = clusterenroll.JoinCSR(ctx, &opts, csrDER, "")
			if noiseRefused(t, suite, err) {
				return
			}
			var refused *clusterenroll.JoinRefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("JoinCSR with a bad token = %v, want *JoinRefusedError", err)
			}
		})
	}
}

func TestStartJoinServer_RefusesMisconfiguration(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := kvstoretest.NewGuardedMemoryStore()
	var nilProvider *clusterenroll.NilProviderError
	if _, err := clusterenroll.StartJoinServer(nil, clusterenroll.JoinServerOptions{Host: "127.0.0.1", Store: store, TrustDomain: trustDomain}, t.TempDir(), logger); !errors.As(err, &nilProvider) {
		t.Fatalf("nil provider = %v, want *NilProviderError", err)
	}

	provider := clusterca.NewProvider(store, plainBarrier{})
	var noStore *clusterenroll.MissingJoinTokenStoreError
	if _, err := clusterenroll.StartJoinServer(provider, clusterenroll.JoinServerOptions{Host: "127.0.0.1", TrustDomain: trustDomain}, t.TempDir(), logger); !errors.As(err, &noStore) {
		t.Fatalf("no store = %v, want *MissingJoinTokenStoreError", err)
	}
	var noDomain *clusterenroll.MissingTrustDomainError
	if _, err := clusterenroll.StartJoinServer(provider, clusterenroll.JoinServerOptions{Host: "127.0.0.1", Store: store}, t.TempDir(), logger); !errors.As(err, &noDomain) {
		t.Fatalf("no trust domain = %v, want *MissingTrustDomainError", err)
	}
}
