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

package clusterenroll

import (
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flynn/noise"
	"github.com/jeremyhahn/go-truststrap/pkg/noiseproto"
	noiseboot "github.com/jeremyhahn/go-truststrap/pkg/noiseproto/bootstrap"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterca"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// mockBarrier implements seal.EncryptionBarrier with real AES-GCM
// round-trips, mirroring pkg/cakey's own test double.
type mockBarrier struct {
	mu     sync.RWMutex
	sealed bool
	key    []byte
}

func newMockBarrier() *mockBarrier {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		panic("test key generation: " + err.Error())
	}
	return &mockBarrier{key: key}
}

func (b *mockBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	// #nosec G407 -- NewGCMWithRandomNonce samples its own nonce; nil is required here.
	return gcm.Seal(nil, nil, plaintext, nil), nil
}

func (b *mockBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nil, ciphertext, nil)
}

func (b *mockBarrier) IsSealed() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.sealed
}

// newTestClusterCAProvider returns an initialized Key-A provider over a
// fresh in-memory store, standing in for "the replicated store, already
// unsealed" every admitted member reads Key A through.
func newTestClusterCAProvider(t *testing.T) *cakey.SoftwareProvider {
	t.Helper()
	p := clusterca.NewProvider(kvstore.NewMemoryKVStore(), newMockBarrier())
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize cluster CA: %v", err)
	}
	return p
}

func TestJoinServer_SignsAValidCSR(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	port := freePort(t)
	js, err := StartJoinServer(provider, JoinServerOptions{
		Host: testBindHost, Port: port, Token: "s3cret-token",
	}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer: %v", err)
	}
	t.Cleanup(js.Stop)

	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	csrDER, err := buildJoinCSR(nodeKey, "spiffe://test.cluster/node/127.0.0.1:9100")
	if err != nil {
		t.Fatalf("buildJoinCSR: %v", err)
	}

	certPEM, err := JoinCSR(context.Background(), &JoinClientOptions{
		ServerAddr: js.Addr(),
		ServerKey:  js.PublicKey(),
		Token:      "s3cret-token",
	}, csrDER, "spiffe://test.cluster/node/127.0.0.1:9100")
	if err != nil {
		t.Fatalf("JoinCSR: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("JoinCSR returned no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	caCert, err := provider.Certificate(context.Background())
	if err != nil {
		t.Fatalf("provider.Certificate: %v", err)
	}
	if err := cert.CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("issued leaf does not chain to the cluster CA: %v", err)
	}
	gotPub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !gotPub.Equal(&nodeKey.PublicKey) {
		t.Fatal("issued leaf does not carry the joiner's own public key")
	}
	if len(cert.URIs) != 1 || cert.URIs[0].String() != "spiffe://test.cluster/node/127.0.0.1:9100" {
		t.Fatalf("leaf URIs = %v, want the joiner's SPIFFE node URI", cert.URIs)
	}
}

func TestJoinServer_WrongTokenRefused(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	port := freePort(t)
	js, err := StartJoinServer(provider, JoinServerOptions{
		Host: testBindHost, Port: port, Token: "correct-token",
	}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer: %v", err)
	}
	t.Cleanup(js.Stop)

	nodeKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, err := buildJoinCSR(nodeKey, "")
	if err != nil {
		t.Fatalf("buildJoinCSR: %v", err)
	}

	_, err = JoinCSR(context.Background(), &JoinClientOptions{
		ServerAddr: js.Addr(),
		ServerKey:  js.PublicKey(),
		Token:      "wrong-token",
	}, csrDER, "")
	var refused *JoinRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("JoinCSR with wrong token = %v, want *JoinRefusedError", err)
	}
}

func TestJoinServer_InvalidCSRRefused(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	port := freePort(t)
	js, err := StartJoinServer(provider, JoinServerOptions{
		Host: testBindHost, Port: port, Token: "tok",
	}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer: %v", err)
	}
	t.Cleanup(js.Stop)

	_, err = JoinCSR(context.Background(), &JoinClientOptions{
		ServerAddr: js.Addr(),
		ServerKey:  js.PublicKey(),
		Token:      "tok",
	}, []byte("not a csr"), "")
	var refused *JoinRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("JoinCSR with garbage CSR = %v, want *JoinRefusedError", err)
	}
}

func TestStartJoinServer_RequiresToken(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	_, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost}, t.TempDir(), discardLogger())
	var tokenErr *MissingJoinTokenError
	if !errors.As(err, &tokenErr) {
		t.Fatalf("StartJoinServer with no token = %v, want *MissingJoinTokenError", err)
	}
}

func TestStartJoinServer_RequiresHost(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	_, err := StartJoinServer(provider, JoinServerOptions{Token: "tok"}, t.TempDir(), discardLogger())
	var hostErr *MissingBindHostError
	if !errors.As(err, &hostErr) {
		t.Fatalf("StartJoinServer with no host = %v, want *MissingBindHostError", err)
	}
}

func TestStartJoinServer_RequiresProvider(t *testing.T) {
	_, err := StartJoinServer(nil, JoinServerOptions{Host: testBindHost, Token: "tok"}, t.TempDir(), discardLogger())
	var nilErr *NilProviderError
	if !errors.As(err, &nilErr) {
		t.Fatalf("StartJoinServer with nil provider = %v, want *NilProviderError", err)
	}
}

func TestJoinCSR_RequiresToken(t *testing.T) {
	_, err := JoinCSR(context.Background(), &JoinClientOptions{ServerAddr: "127.0.0.1:1"}, []byte{}, "")
	var tokenErr *MissingJoinTokenError
	if !errors.As(err, &tokenErr) {
		t.Fatalf("JoinCSR with no token = %v, want *MissingJoinTokenError", err)
	}
}

func TestJoinCSR_ErrorStrings(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"MissingJoinTokenError", &MissingJoinTokenError{}},
		{"NilProviderError", &NilProviderError{}},
		{"JoinRefusedError", &JoinRefusedError{Reason: "bad token"}},
		{"handshakeIncompleteError", ErrHandshakeIncomplete},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Error() == "" {
				t.Fatal("Error() must not be empty")
			}
		})
	}
}

func TestJoinServerOptions_PortDefault(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	js, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Token: "tok"}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer: %v", err)
	}
	defer js.Stop()
	if !strings.HasSuffix(js.Addr(), ":"+strconv.Itoa(DefaultJoinPort)) {
		t.Fatalf("Addr() = %q, want it to end with the default join port %d", js.Addr(), DefaultJoinPort)
	}
}

func TestJoinServer_NilReceiver(t *testing.T) {
	var js *JoinServer
	if js.Addr() != "" {
		t.Fatal("Addr() on a nil JoinServer must be empty")
	}
	if js.PublicKey() != nil {
		t.Fatal("PublicKey() on a nil JoinServer must be nil")
	}
	js.Stop() // must not panic
}

func TestLoadOrGenerateJoinKey_ReusesExistingKey(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	dir := t.TempDir()

	port1 := freePort(t)
	js1, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Port: port1, Token: "tok"}, dir, discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer (first): %v", err)
	}
	// Copied before Stop: Stop destroys the key's protected pages, and
	// PublicKey's slice aliases them.
	pub1 := bytes.Clone(js1.PublicKey())
	js1.Stop()

	port2 := freePort(t)
	js2, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Port: port2, Token: "tok"}, dir, discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer (second, same dir): %v", err)
	}
	defer js2.Stop()
	pub2 := js2.PublicKey()

	if !bytes.Equal(pub1, pub2) {
		t.Fatal("second StartJoinServer over the same data dir generated a NEW key instead of reusing join-key.hex")
	}
}

func TestJoin_AnchorFetchFailure(t *testing.T) {
	opts := &Options{
		RaftAddress: "127.0.0.1:63097",
		TrustDomain: "test.cluster",
		DataDir:     t.TempDir(),
		Bootstrap: BootstrapOptions{
			DirectURL: "https://127.0.0.1:1/bundle", // unreachable
			JoinAddr:  "127.0.0.1:1",
			JoinKey:   "aa",
			JoinToken: "tok",
		},
	}
	_, err := Join(context.Background(), opts, discardLogger())
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("Join with an unreachable anchor = %v, want *FetchError", err)
	}
}

func TestJoin_InvalidJoinKeyHex(t *testing.T) {
	// A real anchor so the failure under test is the hex decode, not the
	// anchor fetch: start a first node purely to serve the anchor.
	_, boot := startFirstNode(t)

	opts := &Options{
		RaftAddress: "127.0.0.1:63098",
		TrustDomain: "test.cluster",
		DataDir:     t.TempDir(),
		Bootstrap: BootstrapOptions{
			NoiseKey:  boot.NoiseKey,
			NoiseAddr: boot.NoiseAddr,
			JoinAddr:  "127.0.0.1:1",
			JoinKey:   "not-valid-hex",
			JoinToken: "tok",
		},
	}

	_, err := Join(context.Background(), opts, discardLogger())
	var decodeErr *JoinKeyDecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("Join with invalid JoinKey hex = %v, want *JoinKeyDecodeError", err)
	}
}

func TestJoinKeyDecodeError_ErrorAndUnwrap(t *testing.T) {
	inner := errors.New("encoding/hex: invalid byte")
	err := &JoinKeyDecodeError{Err: inner}
	if err.Error() == "" {
		t.Fatal("Error() must not be empty")
	}
	if err.Unwrap() != inner {
		t.Fatalf("Unwrap() = %v, want %v", err.Unwrap(), inner)
	}
}

func TestJoinServer_GarbageBeforeHandshakeDoesNotPanic(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	port := freePort(t)
	js, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Port: port, Token: "tok"}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer: %v", err)
	}
	defer js.Stop()

	conn, err := net.DialTimeout("tcp", js.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	// A 2-byte length prefix claiming a short garbage frame as handshake
	// message 1. The server must reject it and close, not panic.
	garbage := []byte{0x00, 0x04, 0xde, 0xad, 0xbe, 0xef}
	if _, err := conn.Write(garbage); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	_, _ = conn.Read(buf) // either EOF or a frame; either is fine, just must not hang/panic
}

func TestJoinCSR_WrongServerKeyLength(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	port := freePort(t)
	js, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Port: port, Token: "tok"}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer: %v", err)
	}
	defer js.Stop()

	_, err = JoinCSR(context.Background(), &JoinClientOptions{
		ServerAddr: js.Addr(),
		ServerKey:  []byte{0x01, 0x02}, // wrong length
		Token:      "tok",
		Timeout:    2 * time.Second,
	}, []byte{}, "")
	if err == nil {
		t.Fatal("JoinCSR with a malformed server key succeeded, want an error")
	}
}

func TestBuildJoinCSR_InvalidNodeURI(t *testing.T) {
	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	_, err = buildJoinCSR(nodeKey, "%zz")
	if err == nil {
		t.Fatal("buildJoinCSR with an unparseable node URI succeeded, want an error")
	}
}

func TestStartJoinServer_PortInUse(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	port := freePort(t)
	dir := t.TempDir()

	js1, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Port: port, Token: "tok"}, dir, discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer (first): %v", err)
	}
	defer js1.Stop()

	_, err = StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Port: port, Token: "tok"}, t.TempDir(), discardLogger())
	var serverErr *ServerError
	if !errors.As(err, &serverErr) {
		t.Fatalf("StartJoinServer on an in-use port = %v, want *ServerError", err)
	}
}

func TestLoadOrGenerateJoinKey_ReadFailure(t *testing.T) {
	dir := t.TempDir()
	keyPath := dir + "/" + JoinKeyFile
	if err := os.WriteFile(keyPath, []byte("not valid hex at all"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := loadOrGenerateJoinKey(keyPath, discardLogger())
	var serverErr *ServerError
	if !errors.As(err, &serverErr) {
		t.Fatalf("loadOrGenerateJoinKey with corrupt key file = %v, want *ServerError", err)
	}
}

// signingFailureProvider is a cakey.Provider double whose Signer always
// fails, for exercising handleJoinRequest's issuance-failure branch.
type signingFailureProvider struct {
	cert *x509.Certificate
}

func (p *signingFailureProvider) Signer(context.Context) (crypto.Signer, error) {
	return nil, errors.New("device unreachable")
}

func (p *signingFailureProvider) Certificate(context.Context) (*x509.Certificate, error) {
	return p.cert, nil
}

func TestJoinServer_SigningFailureRefused(t *testing.T) {
	realProvider := newTestClusterCAProvider(t)
	caCert, err := realProvider.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}

	port := freePort(t)
	js, err := StartJoinServer(&signingFailureProvider{cert: caCert}, JoinServerOptions{
		Host: testBindHost, Port: port, Token: "tok",
	}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer: %v", err)
	}
	defer js.Stop()

	nodeKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, err := buildJoinCSR(nodeKey, "")
	if err != nil {
		t.Fatalf("buildJoinCSR: %v", err)
	}

	_, err = JoinCSR(context.Background(), &JoinClientOptions{
		ServerAddr: js.Addr(),
		ServerKey:  js.PublicKey(),
		Token:      "tok",
	}, csrDER, "")
	var refused *JoinRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("JoinCSR against a failing signer = %v, want *JoinRefusedError", err)
	}
}

// TestJoinServer_GarbageAfterHandshakeDoesNotPanic completes a genuine
// Noise_NK handshake as a raw client, then sends an undecryptable
// ciphertext frame, exercising handle's Decrypt-failure branch.
func TestJoinServer_GarbageAfterHandshakeDoesNotPanic(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	port := freePort(t)
	js, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Port: port, Token: "tok"}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer: %v", err)
	}
	defer js.Stop()

	conn, err := net.DialTimeout("tcp", js.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	sess, err := noiseproto.NewSession(&noiseproto.SessionConfig{
		Pattern:       noise.HandshakeNK,
		PeerStaticKey: js.PublicKey(),
		IsInitiator:   true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := sess.InitHandshake(); err != nil {
		t.Fatalf("InitHandshake: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)

	msg1, _, err := sess.HandshakeMessage(nil)
	if err != nil {
		t.Fatalf("HandshakeMessage (msg1): %v", err)
	}
	if err := noiseboot.WriteFrame(conn, msg1, deadline); err != nil {
		t.Fatalf("WriteFrame (msg1): %v", err)
	}
	msg2, err := noiseboot.ReadFrame(conn, deadline)
	if err != nil {
		t.Fatalf("ReadFrame (msg2): %v", err)
	}
	if _, done, err := sess.HandshakeMessage(msg2); err != nil || !done {
		t.Fatalf("HandshakeMessage (msg2): done=%v err=%v", done, err)
	}

	// A frame the server will successfully READ but fail to DECRYPT.
	if err := noiseboot.WriteFrame(conn, []byte("not a real ciphertext!!"), deadline); err != nil {
		t.Fatalf("WriteFrame (garbage ciphertext): %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	_, _ = conn.Read(buf) // expect EOF; must not hang or panic
}

func TestJoinCSR_DialFailureIsFetchError(t *testing.T) {
	_, err := JoinCSR(context.Background(), &JoinClientOptions{
		ServerAddr: "127.0.0.1:1",
		ServerKey:  make([]byte, 32),
		Token:      "tok",
		Timeout:    200 * time.Millisecond,
	}, []byte{}, "")
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("JoinCSR dial failure = %v, want *FetchError", err)
	}
}
