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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flynn/noise"
	"github.com/jeremyhahn/go-truststrap/pkg/noiseproto"
	noiseboot "github.com/jeremyhahn/go-truststrap/pkg/noiseproto/bootstrap"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterca"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
)

// testTrustDomain is the SPIFFE trust domain every join test in this
// package pins. Minted tokens are bound to spiffe://testTrustDomain/node/*
// identities, and every JoinServerOptions here configures the same domain.
const testTrustDomain = "test.cluster"

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

// seal marks the barrier sealed, for a test proving behavior while Key A is
// unreachable.
func (b *mockBarrier) seal() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sealed = true
}

// newTestClusterCAProvider returns an initialized Key-A provider over a
// fresh in-memory store, standing in for "the replicated store, already
// unsealed" every admitted member reads Key A through. It also resets the
// package-level join-attempt guard (see resetJoinAttemptGuardForTest):
// every test in this file that dials a real JoinServer calls this first,
// so folding the reset in here means one edit covers all of them rather
// than each test remembering to call it separately.
func newTestClusterCAProvider(t *testing.T) *cakey.SoftwareProvider {
	t.Helper()
	resetJoinAttemptGuardForTest(t)
	p := clusterca.NewProvider(kvstoretest.NewGuardedMemoryStore(), newMockBarrier())
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize cluster CA: %v", err)
	}
	return p
}

// resetJoinAttemptGuardForTest installs a fresh package-level join-attempt
// guard for the duration of one test and restores the previous one on
// cleanup. Every test here that dials a real JoinServer over loopback TCP
// shares testBindHost as its source address, so without this reset they
// would exhaust each other's rate-limit/lockout state across the whole
// package's test run (AllowJoinAttempt/RecordJoinAttemptFailure are a
// process-wide singleton by design -- see join_attempt_guard.go) rather
// than each test getting a clean slate.
func resetJoinAttemptGuardForTest(t *testing.T) {
	t.Helper()
	t.Cleanup(ResetJoinAttemptGuardForTest())
}

// mintTestJoinToken mints a real, single-use join token bound to
// spiffe://testTrustDomain/node/nodeID into store, for a test's client side
// to present. It fails the test outright on a mint error, since every
// caller needs a usable token to continue.
func mintTestJoinToken(t *testing.T, store kvstore.AtomicKVStore, nodeID string) string {
	t.Helper()
	token, _, err := MintJoinToken(context.Background(), store, testTrustDomain, nodeID, time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	return token
}

// buildMatchingJoinCSR generates a fresh ECDSA key and a CSR whose sole URI
// SAN is nodeURI, for a test's JoinRequest.NodeURI to match -- the pairing
// validateJoinCSRIdentity now requires of every request before the token is
// ever touched. It fails the test outright on a build error.
func buildMatchingJoinCSR(t *testing.T, nodeURI string) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	csrDER, err := buildJoinCSR(nodeKey, nodeURI)
	if err != nil {
		t.Fatalf("buildJoinCSR: %v", err)
	}
	return nodeKey, csrDER
}

// fakeFeatureAdvertiser records every AdvertiseJoiner call, optionally
// failing it, for tests to prove HandleJoinRequest calls it (or doesn't)
// with the expected arguments.
type fakeFeatureAdvertiser struct {
	mu        sync.Mutex
	calls     int
	replicaID uint64
	raftAddr  string
	features  []string
	err       error
}

func (f *fakeFeatureAdvertiser) AdvertiseJoiner(_ context.Context, replicaID uint64, raftAddress string, features []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.replicaID = replicaID
	f.raftAddr = raftAddress
	f.features = features
	return f.err
}

func (f *fakeFeatureAdvertiser) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestHandleJoinRequest_AdvertisesTheJoinerOnSuccess proves a successful
// join calls FeatureAdvertiser.AdvertiseJoiner with the request's replica ID
// and claimed features, and the raft address recovered from the token's
// bound identity (not the joiner's self-reported NodeURI) -- this is the fix
// for the admission bug: once this record exists, featuregate.Gate.Supports
// finds it and member add is no longer refused.
func TestHandleJoinRequest_AdvertisesTheJoinerOnSuccess(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	nodeURI := spiffe.NodeURI(testTrustDomain, "10.0.0.4:63001")
	token := mintTestJoinToken(t, store, "10.0.0.4:63001")

	_, csrDER := buildMatchingJoinCSR(t, nodeURI)

	adv := &fakeFeatureAdvertiser{}
	resp := HandleJoinRequest(context.Background(), provider, store, testTrustDomain, &JoinRequest{
		Token:     token,
		CSRDER:    csrDER,
		NodeURI:   nodeURI,
		ReplicaID: 4,
		Features:  []string{"guarded-writes"},
	}, adv)
	if resp.Error != "" {
		t.Fatalf("HandleJoinRequest refused: %s", resp.Error)
	}
	if adv.callCount() != 1 {
		t.Fatalf("AdvertiseJoiner calls = %d, want 1", adv.callCount())
	}
	if adv.replicaID != 4 {
		t.Errorf("AdvertiseJoiner replicaID = %d, want 4", adv.replicaID)
	}
	if adv.raftAddr != "10.0.0.4:63001" {
		t.Errorf("AdvertiseJoiner raftAddress = %q, want %q", adv.raftAddr, "10.0.0.4:63001")
	}
	if len(adv.features) != 1 || adv.features[0] != "guarded-writes" {
		t.Errorf("AdvertiseJoiner features = %v, want [guarded-writes]", adv.features)
	}
}

// TestHandleJoinRequest_NoAdvertiserConfigured proves a nil FeatureAdvertiser
// is simply skipped -- the join still succeeds, matching the behavior before
// this parameter existed.
func TestHandleJoinRequest_NoAdvertiserConfigured(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	nodeURI := spiffe.NodeURI(testTrustDomain, "10.0.0.5:63001")
	token := mintTestJoinToken(t, store, "10.0.0.5:63001")

	_, csrDER := buildMatchingJoinCSR(t, nodeURI)

	resp := HandleJoinRequest(context.Background(), provider, store, testTrustDomain, &JoinRequest{
		Token:     token,
		CSRDER:    csrDER,
		NodeURI:   nodeURI,
		ReplicaID: 5,
		Features:  []string{"guarded-writes"},
	}, nil)
	if resp.Error != "" {
		t.Fatalf("HandleJoinRequest refused: %s", resp.Error)
	}
	if len(resp.CertPEM) == 0 {
		t.Fatal("expected a certificate even with no advertiser configured")
	}
}

// TestHandleJoinRequest_ZeroReplicaIDSkipsAdvertising proves a join that
// carries no replica ID (an older joiner binary, or one that genuinely does
// not know it yet) is not advertised for -- there is no key to write it
// under -- without refusing the join itself.
func TestHandleJoinRequest_ZeroReplicaIDSkipsAdvertising(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	nodeURI := spiffe.NodeURI(testTrustDomain, "10.0.0.6:63001")
	token := mintTestJoinToken(t, store, "10.0.0.6:63001")

	_, csrDER := buildMatchingJoinCSR(t, nodeURI)

	adv := &fakeFeatureAdvertiser{}
	resp := HandleJoinRequest(context.Background(), provider, store, testTrustDomain, &JoinRequest{
		Token:    token,
		CSRDER:   csrDER,
		NodeURI:  nodeURI,
		Features: []string{"guarded-writes"},
	}, adv)
	if resp.Error != "" {
		t.Fatalf("HandleJoinRequest refused: %s", resp.Error)
	}
	if adv.callCount() != 0 {
		t.Fatalf("AdvertiseJoiner calls = %d, want 0 (ReplicaID was zero)", adv.callCount())
	}
}

// TestHandleJoinRequest_AdvertiserFailureDoesNotRefuseTheJoin proves a
// failed advertisement is logged, not fatal: the certificate HandleJoinRequest
// already signed and admitted is still returned, since the operator's
// member add simply surfaces the same refusal it always would with no
// advertiser configured at all.
func TestHandleJoinRequest_AdvertiserFailureDoesNotRefuseTheJoin(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	nodeURI := spiffe.NodeURI(testTrustDomain, "10.0.0.7:63001")
	token := mintTestJoinToken(t, store, "10.0.0.7:63001")

	_, csrDER := buildMatchingJoinCSR(t, nodeURI)

	adv := &fakeFeatureAdvertiser{err: errors.New("advertisement store unavailable")}
	resp := HandleJoinRequest(context.Background(), provider, store, testTrustDomain, &JoinRequest{
		Token:     token,
		CSRDER:    csrDER,
		NodeURI:   nodeURI,
		ReplicaID: 7,
	}, adv)
	if resp.Error != "" {
		t.Fatalf("HandleJoinRequest refused: %s", resp.Error)
	}
	if len(resp.CertPEM) == 0 {
		t.Fatal("expected a certificate despite the advertiser failing")
	}
	if adv.callCount() != 1 {
		t.Fatalf("AdvertiseJoiner calls = %d, want 1", adv.callCount())
	}
}

func TestJoinServer_SignsAValidCSR(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	port := freePort(t)
	js, err := StartJoinServer(provider, JoinServerOptions{
		Host: testBindHost, Port: port, Store: store, TrustDomain: testTrustDomain,
	}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer: %v", err)
	}
	t.Cleanup(js.Stop)

	token := mintTestJoinToken(t, store, "127.0.0.1:9100")

	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	// The CSR's own URI is advisory only -- IssueLeafForKey's template
	// takes nothing from it but the public key (clusterca.IssueLeafForKey's
	// own doc comment); the leaf's actual identity comes from the token.
	csrDER, err := buildJoinCSR(nodeKey, "spiffe://attacker.example/node/anything")
	if err != nil {
		t.Fatalf("buildJoinCSR: %v", err)
	}

	certPEM, err := JoinCSR(context.Background(), &JoinClientOptions{
		ServerAddr:    js.Addr(),
		ServerKey:     js.PublicKey(),
		ServerP256Key: js.P256PublicKey(),
		Token:         token,
	}, csrDER, "spiffe://attacker.example/node/anything")
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
	// B5/B6: the leaf carries the TOKEN'S bound identity, never the
	// request's self-reported NodeURI (which named a different, bogus
	// identity above).
	if len(cert.URIs) != 1 || cert.URIs[0].String() != "spiffe://"+testTrustDomain+"/node/127.0.0.1:9100" {
		t.Fatalf("leaf URIs = %v, want exactly the token's bound identity", cert.URIs)
	}
}

func TestJoinServer_WrongTokenRefused(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	port := freePort(t)
	js, err := StartJoinServer(provider, JoinServerOptions{
		Host: testBindHost, Port: port, Store: store, TrustDomain: testTrustDomain,
	}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer: %v", err)
	}
	t.Cleanup(js.Stop)

	// A real token exists in the store, but the client presents a
	// different (never-minted) one.
	mintTestJoinToken(t, store, "127.0.0.1:9101")

	nodeKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, err := buildJoinCSR(nodeKey, "")
	if err != nil {
		t.Fatalf("buildJoinCSR: %v", err)
	}

	_, err = JoinCSR(context.Background(), &JoinClientOptions{
		ServerAddr:    js.Addr(),
		ServerKey:     js.PublicKey(),
		ServerP256Key: js.P256PublicKey(),
		Token:         "never-minted-token",
	}, csrDER, "")
	var refused *JoinRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("JoinCSR with wrong token = %v, want *JoinRefusedError", err)
	}
}

func TestJoinServer_TokenCannotBeReused(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	port := freePort(t)
	js, err := StartJoinServer(provider, JoinServerOptions{
		Host: testBindHost, Port: port, Store: store, TrustDomain: testTrustDomain,
	}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer: %v", err)
	}
	t.Cleanup(js.Stop)

	nodeURI := spiffe.NodeURI(testTrustDomain, "127.0.0.1:9102")
	token := mintTestJoinToken(t, store, "127.0.0.1:9102")

	_, csrDER := buildMatchingJoinCSR(t, nodeURI)

	if _, err := JoinCSR(context.Background(), &JoinClientOptions{
		ServerAddr: js.Addr(), ServerKey: js.PublicKey(), ServerP256Key: js.P256PublicKey(), Token: token,
	}, csrDER, nodeURI); err != nil {
		t.Fatalf("first JoinCSR (token must be accepted once): %v", err)
	}

	// The SAME claimed identity, re-presented with the (already-consumed)
	// token: this must still fail at ConsumeJoinToken, not at the
	// identity check, so the matching URI pairing is kept here too.
	_, otherCSR := buildMatchingJoinCSR(t, nodeURI)
	_, err = JoinCSR(context.Background(), &JoinClientOptions{
		ServerAddr: js.Addr(), ServerKey: js.PublicKey(), ServerP256Key: js.P256PublicKey(), Token: token,
	}, otherCSR, nodeURI)
	var refused *JoinRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("second JoinCSR with the SAME (already-consumed) token = %v, want *JoinRefusedError", err)
	}
}

func TestJoinServer_InvalidCSRRefused(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	port := freePort(t)
	js, err := StartJoinServer(provider, JoinServerOptions{
		Host: testBindHost, Port: port, Store: store, TrustDomain: testTrustDomain,
	}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer: %v", err)
	}
	t.Cleanup(js.Stop)

	token := mintTestJoinToken(t, store, "127.0.0.1:9103")
	_, err = JoinCSR(context.Background(), &JoinClientOptions{
		ServerAddr:    js.Addr(),
		ServerKey:     js.PublicKey(),
		ServerP256Key: js.P256PublicKey(),
		Token:         token,
	}, []byte("not a csr"), "")
	var refused *JoinRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("JoinCSR with garbage CSR = %v, want *JoinRefusedError", err)
	}
}

func TestStartJoinServer_RequiresTokenStore(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	_, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost, TrustDomain: testTrustDomain}, t.TempDir(), discardLogger())
	var storeErr *MissingJoinTokenStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("StartJoinServer with no token store = %v, want *MissingJoinTokenStoreError", err)
	}
}

func TestStartJoinServer_RequiresTrustDomain(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	_, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Store: store}, t.TempDir(), discardLogger())
	var tdErr *MissingTrustDomainError
	if !errors.As(err, &tdErr) {
		t.Fatalf("StartJoinServer with no trust domain = %v, want *MissingTrustDomainError", err)
	}
}

func TestStartJoinServer_RequiresHost(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	_, err := StartJoinServer(provider, JoinServerOptions{Store: store, TrustDomain: testTrustDomain}, t.TempDir(), discardLogger())
	var hostErr *MissingBindHostError
	if !errors.As(err, &hostErr) {
		t.Fatalf("StartJoinServer with no host = %v, want *MissingBindHostError", err)
	}
}

func TestStartJoinServer_RequiresProvider(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, err := StartJoinServer(nil, JoinServerOptions{Host: testBindHost, Store: store, TrustDomain: testTrustDomain}, t.TempDir(), discardLogger())
	var nilErr *NilProviderError
	if !errors.As(err, &nilErr) {
		t.Fatalf("StartJoinServer with nil provider = %v, want *NilProviderError", err)
	}
}

// TestJoinCSR_EmptyTokenIsAllowed confirms an empty token is no longer
// refused before dialing: it means "submit through the approval queue"
// (see JoinRequest's doc comment), not a missing required value. The dial
// to a closed port still fails, but with *FetchError, never a
// token-related refusal.
func TestJoinCSR_EmptyTokenIsAllowed(t *testing.T) {
	_, err := JoinCSR(context.Background(), &JoinClientOptions{ServerAddr: "127.0.0.1:1", ServerP256Key: newTestP256PublicKey(t)}, []byte{}, "")
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("JoinCSR with no token = %v, want *FetchError (dial failure)", err)
	}
}

func TestJoinCSR_ErrorStrings(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"MissingJoinTokenStoreError", &MissingJoinTokenStoreError{}},
		{"MissingTrustDomainError", &MissingTrustDomainError{Component: "test"}},
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
	store := kvstore.NewMemoryKVStore()
	js, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Store: store, TrustDomain: testTrustDomain}, t.TempDir(), discardLogger())
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
	store := kvstore.NewMemoryKVStore()
	dir := t.TempDir()

	port1 := freePort(t)
	js1, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Port: port1, Store: store, TrustDomain: testTrustDomain}, dir, discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer (first): %v", err)
	}
	// Copied before Stop: Stop destroys the keys' protected pages, and the
	// accessors' slices alias them.
	pub1 := bytes.Clone(js1.PublicKey())
	p256Pub1 := bytes.Clone(js1.P256PublicKey())
	js1.Stop()

	port2 := freePort(t)
	js2, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Port: port2, Store: store, TrustDomain: testTrustDomain}, dir, discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer (second, same dir): %v", err)
	}
	defer js2.Stop()

	// The Noise key exists only where this runtime may use Noise; under FIPS
	// 140-3 mode both listeners hold none, and the P-256 key below is the one
	// whose reuse decides whether a pinned joiner still reaches this member.
	if !bytes.Equal(pub1, js2.PublicKey()) {
		t.Fatal("second StartJoinServer over the same data dir generated a NEW key instead of reusing join-key.hex")
	}
	if len(p256Pub1) == 0 || !bytes.Equal(p256Pub1, js2.P256PublicKey()) {
		t.Fatal("second StartJoinServer over the same data dir generated a NEW P-256 key instead of reusing join-p256-key.hex")
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
			P256Key:   boot.P256Key,
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
	store := kvstore.NewMemoryKVStore()
	port := freePort(t)
	js, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Port: port, Store: store, TrustDomain: testTrustDomain}, t.TempDir(), discardLogger())
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
	store := kvstore.NewMemoryKVStore()
	port := freePort(t)
	js, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Port: port, Store: store, TrustDomain: testTrustDomain}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer: %v", err)
	}
	defer js.Stop()

	// Both suites' keys are malformed, so whichever suite this runtime selects
	// -- Noise, or P-256 under FIPS 140-3 mode -- is handed a bad key.
	_, err = JoinCSR(context.Background(), &JoinClientOptions{
		ServerAddr:    js.Addr(),
		ServerKey:     []byte{0x01, 0x02}, // wrong length
		ServerP256Key: []byte{0x01, 0x02}, // wrong length
		Token:         "tok",
		Timeout:       2 * time.Second,
	}, []byte{}, "")
	if err == nil {
		t.Fatal("JoinCSR with a malformed server key succeeded, want an error")
	}
	var fipsErr *NoiseFIPSUnavailableError
	if errors.As(err, &fipsErr) {
		t.Fatalf("JoinCSR = %v: refused for the suite, never reaching the malformed key under test", err)
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
	store := kvstore.NewMemoryKVStore()
	port := freePort(t)
	dir := t.TempDir()

	js1, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Port: port, Store: store, TrustDomain: testTrustDomain}, dir, discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer (first): %v", err)
	}
	defer js1.Stop()

	_, err = StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Port: port, Store: store, TrustDomain: testTrustDomain}, t.TempDir(), discardLogger())
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

// TestLoadOrGenerateJoinKey_MkdirFails covers the generate path's MkdirAll
// failure: a regular file where the key's parent directory should be makes
// fileExists report the key absent, takes the generate path, and fails at
// MkdirAll -- the join listener's own instance of the same branch
// TestStartAdvertiser_DataDirUnusableIsAServerError covers for the anchor
// advertiser, which shares none of this function's code.
func TestLoadOrGenerateJoinKey_MkdirFails(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	keyPath := filepath.Join(blocked, JoinKeyFile)

	_, err := loadOrGenerateJoinKey(keyPath, discardLogger())
	var serverErr *ServerError
	if !errors.As(err, &serverErr) {
		t.Fatalf("loadOrGenerateJoinKey with an unusable parent directory = %v, want *ServerError", err)
	}
	if serverErr.Protocol != "join" {
		t.Errorf("ServerError.Protocol = %q, want %q", serverErr.Protocol, "join")
	}
}

// TestLoadOrGenerateJoinKey_WriteFails covers the generate path's
// WriteFileAtomic failure: a directory at the key's own path makes
// os.WriteFile (inside WriteFileAtomic's rename) fail.
func TestLoadOrGenerateJoinKey_WriteFails(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, JoinKeyFile)
	if err := os.MkdirAll(keyPath, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	_, err := loadOrGenerateJoinKey(keyPath, discardLogger())
	var serverErr *ServerError
	if !errors.As(err, &serverErr) {
		t.Fatalf("loadOrGenerateJoinKey with a directory at the key path = %v, want *ServerError", err)
	}
}

// signingFailureProvider is a cakey.Provider double whose Signer always
// fails, for exercising HandleJoinRequest's issuance-failure branch.
type signingFailureProvider struct {
	cert *x509.Certificate
}

func (p *signingFailureProvider) Signer(context.Context) (crypto.Signer, error) {
	return nil, errors.New("device unreachable")
}

func (p *signingFailureProvider) Certificate(context.Context) (*x509.Certificate, error) {
	return p.cert, nil
}

// TestHandleJoinRequest_SealedRefusesBeforeConsumingToken proves a sealed
// node refuses a join BEFORE it ever consumes the presented token: if the
// token were consumed first and the seal check discovered only afterward
// (at IssueLeafForKey), a legitimately minted single-use token would be
// burned for a join that could never succeed, and the response would leak
// "this token was valid" to anything probing a sealed node -- a
// token-validity oracle. Checking provider.Signer readiness first means a
// sealed node answers every join attempt identically, valid token or not,
// and the token survives to be presented again once unsealed.
func TestHandleJoinRequest_SealedRefusesBeforeConsumingToken(t *testing.T) {
	store := kvstoretest.NewGuardedMemoryStore()
	barrier := newMockBarrier()
	provider := clusterca.NewProvider(store, barrier)
	if err := provider.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	barrier.seal()

	token := mintTestJoinToken(t, store, "sealed-node-test")
	nodeKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, err := buildJoinCSR(nodeKey, "")
	if err != nil {
		t.Fatalf("buildJoinCSR: %v", err)
	}

	resp := HandleJoinRequest(context.Background(), provider, store, testTrustDomain, &JoinRequest{
		Token:  token,
		CSRDER: csrDER,
	}, nil)
	if resp.Error != "join request could not be processed" {
		t.Fatalf("sealed node response = %q, want the generic infrastructure-failure message", resp.Error)
	}

	// The token must still be usable: a sealed node must not have consumed
	// it. ConsumeJoinToken on an UNSEALED check of the same store proves it
	// is still present (consuming it here, which is fine -- this test is
	// done with it).
	if _, err := ConsumeJoinToken(context.Background(), store, testTrustDomain, token); err != nil {
		t.Fatalf("ConsumeJoinToken after a sealed-node refusal = %v, want the token to still be present", err)
	}
}

// TestHandleJoinRequest_InfrastructureFailureRefused proves a genuine store
// failure (as opposed to a refused token) is reported with the SAME generic
// message as a refused token: it carries no information distinguishing
// "your token was wrong" from "something broke," so it adds nothing to a
// token-validity oracle either.
func TestHandleJoinRequest_InfrastructureFailureRefused(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failGetOnPrefix: joinTokenPrefix}

	nodeURI := spiffe.NodeURI(testTrustDomain, "infra-failure-node")
	_, csrDER := buildMatchingJoinCSR(t, nodeURI)

	resp := HandleJoinRequest(context.Background(), provider, store, testTrustDomain, &JoinRequest{
		Token:   "any-token",
		CSRDER:  csrDER,
		NodeURI: nodeURI,
	}, nil)
	if resp.Error != "join request could not be processed" {
		t.Fatalf("response = %q, want the generic infrastructure-failure message", resp.Error)
	}
}

// TestHandleJoinRequest_InvalidCSRDoesNotConsumeToken proves a malformed
// CSR is refused WITHOUT consuming the presented token: the same token,
// retried with a corrected CSR, must still succeed.
func TestHandleJoinRequest_InvalidCSRDoesNotConsumeToken(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	nodeURI := spiffe.NodeURI(testTrustDomain, "node-retry-1")
	token := mintTestJoinToken(t, store, "node-retry-1")

	resp := HandleJoinRequest(context.Background(), provider, store, testTrustDomain, &JoinRequest{
		Token:  token,
		CSRDER: []byte("not a csr at all"),
	}, nil)
	if resp.Error != "invalid certificate signing request" {
		t.Fatalf("response = %q, want the invalid-CSR message", resp.Error)
	}

	_, csrDER := buildMatchingJoinCSR(t, nodeURI)
	resp2 := HandleJoinRequest(context.Background(), provider, store, testTrustDomain, &JoinRequest{
		Token:   token,
		CSRDER:  csrDER,
		NodeURI: nodeURI,
	}, nil)
	if resp2.Error != "" || len(resp2.CertPEM) == 0 {
		t.Fatalf("retry with the SAME token after a malformed CSR = %+v, want a successfully issued leaf", resp2)
	}
}

// TestHandleJoinRequest_ProofOfPossessionFailureDoesNotConsumeToken proves
// a CSR whose self-signature does not check out is refused WITHOUT
// consuming the presented token, for the same reason: the problem is this
// attempt's CSR, not the token.
func TestHandleJoinRequest_ProofOfPossessionFailureDoesNotConsumeToken(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	nodeURI := spiffe.NodeURI(testTrustDomain, "node-retry-2")
	token := mintTestJoinToken(t, store, "node-retry-2")

	_, csrDER := buildMatchingJoinCSR(t, nodeURI)
	// Corrupt the trailing signature bytes: the CSR still parses (the
	// ASN.1 structure and lengths are unchanged), but CheckSignature must
	// now fail.
	corrupted := bytes.Clone(csrDER)
	corrupted[len(corrupted)-1] ^= 0xFF

	resp := HandleJoinRequest(context.Background(), provider, store, testTrustDomain, &JoinRequest{
		Token:   token,
		CSRDER:  corrupted,
		NodeURI: nodeURI,
	}, nil)
	if resp.Error != "certificate signing request: proof of possession failed" {
		t.Fatalf("response = %q, want the proof-of-possession message", resp.Error)
	}

	resp2 := HandleJoinRequest(context.Background(), provider, store, testTrustDomain, &JoinRequest{
		Token:   token,
		CSRDER:  csrDER,
		NodeURI: nodeURI,
	}, nil)
	if resp2.Error != "" || len(resp2.CertPEM) == 0 {
		t.Fatalf("retry with the SAME token after a PoP failure = %+v, want a successfully issued leaf", resp2)
	}
}

// signingFailureSigner is a crypto.Signer whose Sign always fails, standing
// in for a transient device/network fault during issuance -- distinct from
// signingFailureProvider, whose Signer() call itself fails (caught by
// HandleJoinRequest's preflight check, before the token is ever touched).
type signingFailureSigner struct {
	pub crypto.PublicKey
}

func (s *signingFailureSigner) Public() crypto.PublicKey { return s.pub }

func (s *signingFailureSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("device unreachable mid-signature")
}

// issuanceFailureProvider is a cakey.Provider double whose Signer()
// SUCCEEDS (passing HandleJoinRequest's preflight check) but whose
// returned Signer fails on Sign -- the scenario HandleJoinRequest's
// reordering exists for: a failure DURING issuance, after the token has
// already been consumed.
type issuanceFailureProvider struct {
	cert *x509.Certificate
}

func (p *issuanceFailureProvider) Signer(context.Context) (crypto.Signer, error) {
	return &signingFailureSigner{pub: p.cert.PublicKey}, nil
}

func (p *issuanceFailureProvider) Certificate(context.Context) (*x509.Certificate, error) {
	return p.cert, nil
}

// TestHandleJoinRequest_SignFailureAfterConsumeLeavesIdentityRetryable is
// the owner-required proof for the consume-before-mark ordering: a
// signing failure that happens AFTER the token is consumed (CSR and PoP
// already validated) must not retire the node identity -- a fresh token
// minted for the SAME node ID must still succeed once signing works again.
func TestHandleJoinRequest_SignFailureAfterConsumeLeavesIdentityRetryable(t *testing.T) {
	realProvider := newTestClusterCAProvider(t)
	caCert, err := realProvider.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	failingProvider := &issuanceFailureProvider{cert: caCert}

	store := kvstore.NewMemoryKVStore()
	nodeURI := spiffe.NodeURI(testTrustDomain, "node-retry-3")
	token := mintTestJoinToken(t, store, "node-retry-3")
	nodeKey, csrDER := buildMatchingJoinCSR(t, nodeURI)

	resp := HandleJoinRequest(context.Background(), failingProvider, store, testTrustDomain, &JoinRequest{
		Token:   token,
		CSRDER:  csrDER,
		NodeURI: nodeURI,
	}, nil)
	if resp.Error != "signing failed" {
		t.Fatalf("response = %+v, want the signing-failed message", resp)
	}

	// The node identity must NOT be retired: a freshly minted token for
	// the SAME node ID, against a working provider, succeeds.
	retryToken := mintTestJoinToken(t, store, "node-retry-3")
	retryCSRDER, err := buildJoinCSR(nodeKey, nodeURI)
	if err != nil {
		t.Fatalf("buildJoinCSR (retry): %v", err)
	}
	resp2 := HandleJoinRequest(context.Background(), realProvider, store, testTrustDomain, &JoinRequest{
		Token:   retryToken,
		CSRDER:  retryCSRDER,
		NodeURI: nodeURI,
	}, nil)
	if resp2.Error != "" || len(resp2.CertPEM) == 0 {
		t.Fatalf("retry for the same node ID after a sign failure = %+v, want a successfully issued leaf", resp2)
	}
}

// TestHandleJoinRequest_ConcurrentTokensForOneIdentityYieldExactlyOneLeaf
// is the owner-required concurrency proof for the reservation ordering:
// two tokens independently and validly minted for the SAME node identity
// -- both still unconsumed, so both pass ConsumeJoinToken -- race each
// other into HandleJoinRequest at once. ReserveJoinIdentity's atomic
// claim, not an unordered consume-then-mark, is what must make exactly
// ONE of them win.
func TestHandleJoinRequest_ConcurrentTokensForOneIdentityYieldExactlyOneLeaf(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()

	const nodeID = "node-race-1"
	nodeURI := spiffe.NodeURI(testTrustDomain, nodeID)
	tokenOne := mintTestJoinToken(t, store, nodeID)
	tokenTwo := mintTestJoinToken(t, store, nodeID)

	_, csrOne := buildMatchingJoinCSR(t, nodeURI)
	_, csrTwo := buildMatchingJoinCSR(t, nodeURI)

	var wg sync.WaitGroup
	responses := make([]*JoinResponse, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		responses[0] = HandleJoinRequest(context.Background(), provider, store, testTrustDomain, &JoinRequest{
			Token:   tokenOne,
			CSRDER:  csrOne,
			NodeURI: nodeURI,
		}, nil)
	}()
	go func() {
		defer wg.Done()
		responses[1] = HandleJoinRequest(context.Background(), provider, store, testTrustDomain, &JoinRequest{
			Token:   tokenTwo,
			CSRDER:  csrTwo,
			NodeURI: nodeURI,
		}, nil)
	}()
	wg.Wait()

	leaves := 0
	refusals := 0
	for i, resp := range responses {
		switch {
		case len(resp.CertPEM) > 0 && resp.Error == "":
			leaves++
		case resp.Error != "":
			refusals++
		default:
			t.Fatalf("response %d = %+v, want either a leaf or an error, not both/neither", i, resp)
		}
	}
	if leaves != 1 {
		t.Fatalf("got %d issued leaves for two concurrent tokens on one identity, want exactly 1 (refusals=%d)", leaves, refusals)
	}
	if refusals != 1 {
		t.Fatalf("got %d refusals, want exactly 1 (leaves=%d)", refusals, leaves)
	}
}

func TestJoinServer_SigningFailureRefused(t *testing.T) {
	realProvider := newTestClusterCAProvider(t)
	caCert, err := realProvider.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}

	store := kvstore.NewMemoryKVStore()
	port := freePort(t)
	js, err := StartJoinServer(&signingFailureProvider{cert: caCert}, JoinServerOptions{
		Host: testBindHost, Port: port, Store: store, TrustDomain: testTrustDomain,
	}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer: %v", err)
	}
	defer js.Stop()

	token := mintTestJoinToken(t, store, "127.0.0.1:9104")
	nodeKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, err := buildJoinCSR(nodeKey, "")
	if err != nil {
		t.Fatalf("buildJoinCSR: %v", err)
	}

	_, err = JoinCSR(context.Background(), &JoinClientOptions{
		ServerAddr:    js.Addr(),
		ServerKey:     js.PublicKey(),
		ServerP256Key: js.P256PublicKey(),
		Token:         token,
	}, csrDER, "")
	var refused *JoinRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("JoinCSR against a failing signer = %v, want *JoinRefusedError", err)
	}
}

// TestJoinServer_GarbageAfterHandshakeDoesNotPanic completes a genuine
// handshake as a raw client -- Noise_NK, or P-256 under FIPS 140-3 mode -- then
// sends an undecryptable ciphertext frame, exercising handle's
// Decrypt-failure branch.
func TestJoinServer_GarbageAfterHandshakeDoesNotPanic(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	port := freePort(t)
	js, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Port: port, Store: store, TrustDomain: testTrustDomain}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartJoinServer: %v", err)
	}
	defer js.Stop()

	conn, err := net.DialTimeout("tcp", js.Addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	if NoiseAvailable() {
		completeNoiseJoinHandshake(t, conn, js.PublicKey(), deadline)
	} else {
		completeP256JoinHandshake(t, conn, js.P256PublicKey(), deadline)
	}

	// A frame the server will successfully READ but fail to DECRYPT.
	if err := noiseboot.WriteFrame(conn, []byte("not a real ciphertext!!"), deadline); err != nil {
		t.Fatalf("WriteFrame (garbage ciphertext): %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	_, _ = conn.Read(buf) // expect EOF; must not hang or panic
}

// completeNoiseJoinHandshake drives the Noise_NK suite's handshake over conn as
// a raw initiator, leaving the connection at the point the server expects its
// first encrypted frame. It needs X25519, so a caller reaches it only where
// NoiseAvailable() holds.
func completeNoiseJoinHandshake(t *testing.T, conn net.Conn, serverKey []byte, deadline time.Time) {
	t.Helper()
	sess, err := noiseproto.NewSession(&noiseproto.SessionConfig{
		Pattern:       noise.HandshakeNK,
		PeerStaticKey: serverKey,
		IsInitiator:   true,
		Prologue:      noiseboot.NoiseSuiteHeader(),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := sess.InitHandshake(); err != nil {
		t.Fatalf("InitHandshake: %v", err)
	}
	msg1, _, err := sess.HandshakeMessage(nil)
	if err != nil {
		t.Fatalf("HandshakeMessage (msg1): %v", err)
	}
	// The join exchange opens with the version||suite header, as the bootstrap
	// wire does.
	if err := noiseboot.WriteFrame(conn, append(noiseboot.NoiseSuiteHeader(), msg1...), deadline); err != nil {
		t.Fatalf("WriteFrame (msg1): %v", err)
	}
	msg2, err := noiseboot.ReadFrame(conn, deadline)
	if err != nil {
		t.Fatalf("ReadFrame (msg2): %v", err)
	}
	if _, done, err := sess.HandshakeMessage(msg2); err != nil || !done {
		t.Fatalf("HandshakeMessage (msg2): done=%v err=%v", done, err)
	}
}

func TestJoinCSR_DialFailureIsFetchError(t *testing.T) {
	_, err := JoinCSR(context.Background(), &JoinClientOptions{
		ServerAddr:    "127.0.0.1:1",
		ServerP256Key: newTestP256PublicKey(t),
		Token:         "tok",
		Timeout:       200 * time.Millisecond,
	}, []byte{}, "")
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("JoinCSR dial failure = %v, want *FetchError", err)
	}
}

// parseTestJoinCSR builds a CSR bound to nodeURI and parses it, the form
// issueAndAdmit receives once HandleJoinRequest has validated the request.
func parseTestJoinCSR(t *testing.T, nodeURI string) *x509.CertificateRequest {
	t.Helper()
	_, csrDER := buildMatchingJoinCSR(t, nodeURI)
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.Fatalf("ParseCertificateRequest: %v", err)
	}
	return csr
}

func TestIssueAndAdmit_ReservationStoreFailureRefusesWithoutSigning(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failPutIfAbsentOnPrefix: joinIdentityPrefix}
	nodeURI := spiffe.NodeURI(testTrustDomain, "reserve-failure-node")
	advertiser := &fakeFeatureAdvertiser{}

	resp := issueAndAdmit(context.Background(), provider, store, nodeURI, parseTestJoinCSR(t, nodeURI), 7, nil, advertiser)
	if resp.Error != "join request could not be processed" {
		t.Fatalf("response = %q, want the generic infrastructure-failure message", resp.Error)
	}
	if len(resp.CertPEM) != 0 {
		t.Fatal("a leaf was issued although the identity could not be reserved")
	}
	if n := advertiser.callCount(); n != 0 {
		t.Fatalf("advertiser called %d times for a refused join, want 0", n)
	}
	if got, err := store.AtomicKVStore.Get(context.Background(), joinIdentityPrefix+nodeURI); err != nil || got != nil {
		t.Fatalf("identity record = %q (err %v), want none", got, err)
	}
}

func TestIssueAndAdmit_SignFailureWithUnreleasableReservationKeepsItHeld(t *testing.T) {
	realProvider := newTestClusterCAProvider(t)
	caCert, err := realProvider.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	failingProvider := &issuanceFailureProvider{cert: caCert}
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failCompareAndDeleteOnPrefix: joinIdentityPrefix}
	nodeURI := spiffe.NodeURI(testTrustDomain, "release-failure-node")

	resp := issueAndAdmit(context.Background(), failingProvider, store, nodeURI, parseTestJoinCSR(t, nodeURI), 0, nil, nil)
	if resp.Error != "signing failed" {
		t.Fatalf("response = %q, want the signing-failed message", resp.Error)
	}
	if len(resp.CertPEM) != 0 {
		t.Fatal("a leaf was returned although signing failed")
	}

	// The release failed, so the pending reservation is still in place and
	// still live: a second reservation for the same identity is refused
	// until its TTL lapses.
	got, err := store.AtomicKVStore.Get(context.Background(), joinIdentityPrefix+nodeURI)
	if err != nil {
		t.Fatalf("Get identity record: %v", err)
	}
	if deadline, ok := parsePendingDeadline(got); !ok || !deadline.After(time.Now()) {
		t.Fatalf("identity record = %q, want a live pending reservation", got)
	}
	reserved, _, err := ReserveJoinIdentity(context.Background(), store, nodeURI, 0)
	if err != nil || reserved {
		t.Fatalf("ReserveJoinIdentity after the failed release = (%v, %v), want (false, nil)", reserved, err)
	}
}

func TestParseLeafExpiry_RefusesNonPEM(t *testing.T) {
	_, _, err := parseLeafExpiry([]byte("not a pem block"))
	var parseErr *LeafExpiryParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("parseLeafExpiry(non-PEM) = %v, want *LeafExpiryParseError", err)
	}
	if !strings.Contains(err.Error(), "issued leaf is not valid PEM") {
		t.Fatalf("error = %q, want it to name the PEM failure", err)
	}
}

func TestParseLeafExpiry_RefusesUnparsableCertificate(t *testing.T) {
	garbage := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not DER")})
	_, _, err := parseLeafExpiry(garbage)
	var parseErr *LeafExpiryParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("parseLeafExpiry(bad DER) = %v, want *LeafExpiryParseError", err)
	}
	if parseErr.Unwrap() == nil || errors.Unwrap(err) != parseErr.Err {
		t.Fatalf("Unwrap = %v, want the x509 parse error", errors.Unwrap(err))
	}
	if !strings.HasPrefix(err.Error(), "clusterenroll: could not parse the issued leaf's own expiry: ") {
		t.Fatalf("error = %q, want the clusterenroll expiry-parse prefix", err)
	}
}

func TestParseLeafExpiry_ReadsTheIssuedLeaf(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	nodeURI := spiffe.NodeURI(testTrustDomain, "expiry-node")
	certPEM, err := clusterca.IssueLeafForKey(context.Background(), provider, parseTestJoinCSR(t, nodeURI).PublicKey, nodeURI)
	if err != nil {
		t.Fatalf("IssueLeafForKey: %v", err)
	}
	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	notAfter, serial, err := parseLeafExpiry(certPEM)
	if err != nil {
		t.Fatalf("parseLeafExpiry: %v", err)
	}
	if !notAfter.Equal(cert.NotAfter) || serial != cert.SerialNumber.String() {
		t.Fatalf("parseLeafExpiry = (%v, %s), want (%v, %s)", notAfter, serial, cert.NotAfter, cert.SerialNumber)
	}
}

func TestJoinPendingError_NamesTheRequest(t *testing.T) {
	err := &JoinPendingError{RequestID: "req-42"}
	want := "clusterenroll: join request req-42 is awaiting operator approval"
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}
