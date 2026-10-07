// Copyright (c) 2026 Jeremy Hahn
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

// White-box tests for the fetch path: fetching a CA bundle from a live
// advertiser and the truststrap method wiring.

package clusterenroll

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterca"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// freePort returns a TCP port with nothing bound to it.
//
// The advertiser treats NoisePort 0 as "use DefaultNoisePort" rather than
// "let the OS choose", so every advertiser started with the zero value lands
// on the same fixed port. Tests that each start one must therefore pick
// distinct ports explicitly, or a test will silently enroll against another
// test's advertiser -- which is how this file first failed, fetching an
// anchor-only bundle from a neighbouring test and reporting a missing CA key.
//
// The port comes from the shared harness allocator: a "127.0.0.1:0" probe
// closed before the advertiser binds hands out an ephemeral-range number any
// outbound socket -- or another test process -- can take in between.
func freePort(t *testing.T) int {
	t.Helper()
	port, err := qrdbdharness.ReserveTCPPort()
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	return port
}

// testBindHost is the bind host every advertiser in this package's tests
// uses. AdvertiseOptions.NoiseHost has no default -- StartAdvertiser fails
// closed without one (go-qrdb#167) -- and loopback keeps the suite from
// opening a wildcard socket on the build host.
const testBindHost = "127.0.0.1"

// startFirstNode brings up a real first node (path 5): it generates the
// cluster CA and starts the Noise advertiser on a port of its own. The
// returned options are what a joining node needs to reach it.
func startFirstNode(t *testing.T) (*Advertiser, BootstrapOptions) {
	t.Helper()

	port := freePort(t)
	opts := &Options{
		RaftAddress: "127.0.0.1:63001",
		TrustDomain: "test.cluster",
		DataDir:     t.TempDir(),
		SingleNode:  false,
		Advertise:   AdvertiseOptions{NoisePort: port, NoiseHost: testBindHost},
	}

	_, adv, err := Resolve(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("first node Resolve: %v", err)
	}
	if adv == nil {
		t.Fatal("first node must return an advertiser")
	}
	t.Cleanup(adv.Stop)

	return adv, BootstrapOptions{
		NoiseKey: hex.EncodeToString(adv.NoisePublicKey()),
		// Addr() now reports the host the advertiser was actually bound
		// to, so the joining node dials exactly that.
		NoiseAddr: adv.Addr(),
	}
}

// TestFetchAnchorReportsAFetchFailure covers the path where no bootstrap
// method can reach a bundle. It must fail loudly: a node that silently
// continued would fall back to a self-signed identity the cluster does not
// trust.
func TestFetchAnchorReportsAFetchFailure(t *testing.T) {
	opts := &Options{
		RaftAddress: "127.0.0.1:63003",
		TrustDomain: "test.cluster",
		DataDir:     t.TempDir(),
		Bootstrap: BootstrapOptions{
			// A syntactically valid but unreachable endpoint.
			DirectURL: "https://127.0.0.1:1/bundle",
		},
	}

	_, err := FetchAnchor(context.Background(), opts, discardLogger())
	if err == nil {
		t.Fatal("FetchAnchor must fail when no bundle can be fetched")
	}
	var want *FetchError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *FetchError", err, err)
	}
}

// TestAutoFetchBundleWiresEveryConfiguredMethod covers the per-method config
// branches. Each fetch is expected to FAIL (nothing is listening) -- what is
// under test is that the option shape produces a configured method at all
// rather than being silently dropped, which would make an operator's
// --bootstrap-spki flag a no-op that reports no error.
func TestAutoFetchBundleWiresEveryConfiguredMethod(t *testing.T) {
	tests := []struct {
		name      string
		bootstrap BootstrapOptions
	}{
		{
			name:      "spki",
			bootstrap: BootstrapOptions{SPKIPin: "aGVsbG8=", ServerAddr: "127.0.0.1:1"},
		},
		{
			name:      "dane",
			bootstrap: BootstrapOptions{DANEDNS: "127.0.0.1:1", ServerAddr: "127.0.0.1:1"},
		},
		{
			name:      "direct",
			bootstrap: BootstrapOptions{DirectURL: "https://127.0.0.1:1/bundle"},
		},
		{
			name: "several at once",
			bootstrap: BootstrapOptions{
				SPKIPin:    "aGVsbG8=",
				ServerAddr: "127.0.0.1:1",
				DirectURL:  "https://127.0.0.1:1/bundle",
				DANEDNS:    "127.0.0.1:1",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := &Options{
				RaftAddress: "127.0.0.1:63004",
				TrustDomain: "test.cluster",
				DataDir:     t.TempDir(),
				Bootstrap:   tc.bootstrap,
			}
			_, err := autoFetchBundle(context.Background(), opts, discardLogger())
			if err == nil {
				t.Fatal("expected the fetch to fail against a dead endpoint")
			}
			var want *FetchError
			if !errors.As(err, &want) {
				t.Fatalf("error = %v (%T), want *FetchError", err, err)
			}
		})
	}
}

// TestLogAdvertiserInfoToleratesAbsentAdvertisers covers the nil guards. This
// runs on the startup path, so a panic here would take down a node that had
// otherwise enrolled successfully.
func TestLogAdvertiserInfoToleratesAbsentAdvertisers(t *testing.T) {
	logAdvertiserInfo(nil, AdvertiseOptions{})
	logAdvertiserInfo(&Advertiser{}, AdvertiseOptions{})
}

// TestLogAdvertiserInfoDefaultsTheAdminPort covers the zero-port branch, where
// the logged value is the port the server will actually use rather than the
// literal zero the operator did not set.
func TestLogAdvertiserInfoDefaultsTheAdminPort(t *testing.T) {
	adv, _ := startFirstNode(t)

	logAdvertiserInfo(adv, AdvertiseOptions{AdminPort: 0})
	logAdvertiserInfo(adv, AdvertiseOptions{AdminPort: 9443})
}

// clusterMember is one admitted node in TestJoin_SurvivesFirstNodeLoss's
// simulated cluster: an anchor advertiser (for FetchAnchor) plus a join
// listener (for member-side CSR signing), both backed by the SAME Key A
// cakey.Provider every admitted member shares once Key A is replicated.
type clusterMember struct {
	adv *Advertiser
	js  *JoinServer
}

func (m *clusterMember) stop() {
	m.adv.Stop()
	m.js.Stop()
}

func (m *clusterMember) bootstrap(token string) BootstrapOptions {
	return BootstrapOptions{
		NoiseKey:  hex.EncodeToString(m.adv.NoisePublicKey()),
		NoiseAddr: m.adv.Addr(),
		JoinAddr:  m.js.Addr(),
		JoinKey:   hex.EncodeToString(m.js.PublicKey()),
		JoinToken: token,
	}
}

// startClusterMember starts one admitted member's enrollment surface over
// the shared cluster CA provider and the shared join-token store (the SAME
// replicated store a real cluster's members read Key A and join tokens
// through). caCertPEM is the anchor it advertises -- every member
// advertises the SAME certificate, exactly as every member of a real
// cluster does.
func startClusterMember(t *testing.T, provider cakey.Provider, caCertPEM []byte, store kvstore.AtomicKVStore) *clusterMember {
	t.Helper()
	skipIfNoiseUnavailable(t)

	anchorPort := freePort(t)
	adv, err := StartAnchorAdvertiser(caCertPEM, AdvertiseOptions{NoisePort: anchorPort, NoiseHost: testBindHost}, t.TempDir(), discardLogger())
	if err != nil {
		t.Fatalf("StartAnchorAdvertiser: %v", err)
	}

	joinPort := freePort(t)
	js, err := StartJoinServer(provider, JoinServerOptions{Host: testBindHost, Port: joinPort, Store: store, TrustDomain: testTrustDomain}, t.TempDir(), discardLogger())
	if err != nil {
		adv.Stop()
		t.Fatalf("StartJoinServer: %v", err)
	}

	m := &clusterMember{adv: adv, js: js}
	t.Cleanup(m.stop)
	return m
}

// TestJoin_SurvivesFirstNodeLoss is the required end-to-end proof that
// member-side CSR join does not depend on the first node specifically: kill
// it, and a NEW node still joins through a SURVIVING member, receiving a
// leaf that chains to the SAME cluster CA the (now dead) first node
// originally generated.
func TestJoin_SurvivesFirstNodeLoss(t *testing.T) {
	resetJoinAttemptGuardForTest(t)

	// The replicated store every admitted member reads Key A AND join
	// tokens through. Standing in for Raft consensus across real nodes, as
	// the rest of this package's tests already do for "the replicated
	// store" (see pkg/cakey and pkg/clusterca's own mode-agnostic coverage)
	// -- the property under test is that ANY member holding this provider
	// can serve a join, not that Raft replication itself works, which is
	// go-quicraft's own test surface.
	store := kvstoretest.NewGuardedMemoryStore()
	barrier := newMockBarrier()
	provider := clusterca.NewProvider(store, barrier)
	if err := provider.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize cluster CA: %v", err)
	}
	caCert, err := provider.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	caCertPEM, err := clusterca.CACertPEM(context.Background(), provider)
	if err != nil {
		t.Fatalf("CACertPEM: %v", err)
	}

	// Two already-admitted members: the first node, and a second member
	// that joined it at some point in the past (that join is not
	// re-exercised here; what matters is that it is ALSO backed by the
	// shared provider and store, exactly as a real second member reading
	// the replicated store would be).
	firstNode := startClusterMember(t, provider, caCertPEM, store)
	secondMember := startClusterMember(t, provider, caCertPEM, store)

	// Kill the first node.
	firstNode.stop()
	if _, err := net.DialTimeout("tcp", firstNode.js.Addr(), 200*time.Millisecond); err == nil {
		t.Fatal("first node's join listener is still reachable after Stop; the kill was not real")
	}

	// An admin mints a single-use token bound to the new node's identity
	// before it ever dials anyone -- the SAME replicated store either
	// surviving member would check it against.
	joinToken := mintTestJoinToken(t, store, "127.0.0.1:63099")

	// A brand-new, third node joins through the SURVIVING member only.
	opts := &Options{
		RaftAddress: "127.0.0.1:63099",
		TrustDomain: "test.cluster",
		DataDir:     t.TempDir(),
		Bootstrap:   secondMember.bootstrap(joinToken),
	}
	mat, err := Join(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("Join through the surviving member failed: %v", err)
	}
	if len(mat.CAKeyPEM) != 0 {
		t.Fatal("joined material must never carry the cluster CA private key")
	}

	leafBlock, _ := pem.Decode(mat.CertPEM)
	if leafBlock == nil {
		t.Fatal("joined material has no leaf certificate PEM")
	}
	leaf, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if err := leaf.CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("the new node's leaf does not chain to the original cluster CA: %v", err)
	}

	// Local state: the joined node's own cert material is cached, with no
	// CA key file anywhere -- consistent with every other path in this
	// package.
	if _, err := os.Stat(filepath.Join(opts.DataDir, "ca-key.pem")); !os.IsNotExist(err) {
		t.Fatalf("a joined node must never cache a CA key file (stat err = %v)", err)
	}
	role, found, err := loadRole(opts.DataDir)
	if err != nil {
		t.Fatalf("loadRole: %v", err)
	}
	if !found || role != RoleJoined {
		t.Fatalf("role = %q (found=%v), want %q", role, found, RoleJoined)
	}
}

// TestJoin_TokenlessApprovalQueue_PollsUntilApproved drives the full
// client+server loop for a joiner configured with NO join token: Join
// submits the CSR, gets back *JoinPendingError on the wire (not a failure),
// and keeps polling at JoinApprovalPollInterval until an operator calls
// ApproveJoinRequest against the SAME replicated store -- at which point
// the very next poll comes back with a leaf chained to the cluster CA.
func TestJoin_TokenlessApprovalQueue_PollsUntilApproved(t *testing.T) {
	resetJoinAttemptGuardForTest(t)

	store := kvstoretest.NewGuardedMemoryStore()
	barrier := newMockBarrier()
	provider := clusterca.NewProvider(store, barrier)
	if err := provider.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize cluster CA: %v", err)
	}
	caCert, err := provider.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	caCertPEM, err := clusterca.CACertPEM(context.Background(), provider)
	if err != nil {
		t.Fatalf("CACertPEM: %v", err)
	}

	member := startClusterMember(t, provider, caCertPEM, store)
	bootstrap := member.bootstrap("")
	bootstrap.JoinApprovalPollInterval = 20 * time.Millisecond

	opts := &Options{
		RaftAddress: "127.0.0.1:63199",
		TrustDomain: testTrustDomain,
		DataDir:     t.TempDir(),
		Bootstrap:   bootstrap,
	}

	type joinResult struct {
		mat *clustertls.Material
		err error
	}
	resultCh := make(chan joinResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		mat, err := Join(ctx, opts, discardLogger())
		resultCh <- joinResult{mat, err}
	}()

	// Wait for the CSR to actually land in the queue, then approve it --
	// exactly the operator action an admin CLI would trigger.
	deadline := time.Now().Add(5 * time.Second)
	var requestID string
	for time.Now().Before(deadline) {
		pending, err := ListPendingJoinRequests(context.Background(), store)
		if err != nil {
			t.Fatalf("ListPendingJoinRequests: %v", err)
		}
		if len(pending) == 1 {
			requestID = pending[0].ID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if requestID == "" {
		t.Fatal("the tokenless CSR never appeared in the pending queue")
	}
	if _, err := ApproveJoinRequest(context.Background(), store, testTrustDomain, requestID, "63199", "operator-1"); err != nil {
		t.Fatalf("ApproveJoinRequest: %v", err)
	}

	select {
	case res := <-resultCh:
		if res.err != nil {
			t.Fatalf("Join after approval failed: %v", res.err)
		}
		leafBlock, _ := pem.Decode(res.mat.CertPEM)
		if leafBlock == nil {
			t.Fatal("joined material has no leaf certificate PEM")
		}
		leaf, err := x509.ParseCertificate(leafBlock.Bytes)
		if err != nil {
			t.Fatalf("ParseCertificate: %v", err)
		}
		if err := leaf.CheckSignatureFrom(caCert); err != nil {
			t.Fatalf("the approved node's leaf does not chain to the cluster CA: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Join never returned after approval")
	}
}

// TestJoin_SPKIDirectEndToEnd drives Join's SPKI/Direct branch end to end:
// TestJoin_SurvivesFirstNodeLoss above exercises the Noise_NK branch, but
// Join itself has two independent branches (joinNoiseConfigured selects
// between them) and nothing in this package drove the other one through
// Join directly -- only through cmd/qrdbd's own operator-wiring test, whose
// coverage is attributed to cmd/qrdbd's profile, not this package's.
func TestJoin_SPKIDirectEndToEnd(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	caCert, err := provider.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	caCertPEM, err := clusterca.CACertPEM(context.Background(), provider)
	if err != nil {
		t.Fatalf("CACertPEM: %v", err)
	}

	store := kvstore.NewMemoryKVStore()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ca/bootstrap", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(caCertPEM)
	})
	mux.HandleFunc(JoinHTTPPath, func(w http.ResponseWriter, r *http.Request) {
		var req JoinRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		resp := HandleJoinRequest(r.Context(), provider, store, testTrustDomain, &req, nil)
		respBody, err := json.Marshal(resp)
		if err != nil {
			http.Error(w, "marshal failed", http.StatusInternalServerError)
			return
		}
		if resp.Error != "" {
			w.WriteHeader(http.StatusForbidden)
		}
		_, _ = w.Write(respBody)
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	pin := hex.EncodeToString(sum[:])
	serverAddr := strings.TrimPrefix(srv.URL, "https://")
	token := mintTestJoinToken(t, store, "127.0.0.1:63098")

	opts := &Options{
		RaftAddress: "127.0.0.1:63098",
		TrustDomain: "test.cluster",
		DataDir:     t.TempDir(),
		Bootstrap: BootstrapOptions{
			SPKIPin:     pin,
			ServerAddr:  serverAddr,
			JoinHTTPURL: srv.URL,
			JoinToken:   token,
		},
	}
	mat, err := Join(context.Background(), opts, discardLogger())
	if err != nil {
		t.Fatalf("Join (SPKI/Direct): %v", err)
	}
	if len(mat.CAKeyPEM) != 0 {
		t.Fatal("joined material must never carry the cluster CA private key")
	}

	leafBlock, _ := pem.Decode(mat.CertPEM)
	if leafBlock == nil {
		t.Fatal("joined material has no leaf certificate PEM")
	}
	leaf, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if err := leaf.CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("the new node's leaf does not chain to the cluster CA: %v", err)
	}

	role, found, err := loadRole(opts.DataDir)
	if err != nil {
		t.Fatalf("loadRole: %v", err)
	}
	if !found || role != RoleJoined {
		t.Fatalf("role = %q (found=%v), want %q", role, found, RoleJoined)
	}
}

// TestJoin_RefusesLeafForWrongAnchor proves Join verifies the returned leaf
// actually chains to the anchor THIS joiner fetched: the join route here
// signs with a DIFFERENT cluster CA than the one served at /v1/ca/bootstrap
// -- standing in for a signing-member or transport defect that answers a
// join with a leaf for a different cluster entirely -- and Join must refuse
// it with *JoinedLeafVerifyError rather than persisting it.
func TestJoin_RefusesLeafForWrongAnchor(t *testing.T) {
	anchorProvider := newTestClusterCAProvider(t)
	anchorCertPEM, err := clusterca.CACertPEM(context.Background(), anchorProvider)
	if err != nil {
		t.Fatalf("CACertPEM (anchor): %v", err)
	}

	// A second, unrelated cluster CA: the join route signs with THIS one,
	// never the anchor served above.
	wrongProvider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ca/bootstrap", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(anchorCertPEM)
	})
	mux.HandleFunc(JoinHTTPPath, func(w http.ResponseWriter, r *http.Request) {
		var req JoinRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		resp := HandleJoinRequest(r.Context(), wrongProvider, store, testTrustDomain, &req, nil)
		respBody, err := json.Marshal(resp)
		if err != nil {
			http.Error(w, "marshal failed", http.StatusInternalServerError)
			return
		}
		if resp.Error != "" {
			w.WriteHeader(http.StatusForbidden)
		}
		_, _ = w.Write(respBody)
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	pin := hex.EncodeToString(sum[:])
	serverAddr := strings.TrimPrefix(srv.URL, "https://")
	token := mintTestJoinToken(t, store, "127.0.0.1:63099")

	opts := &Options{
		RaftAddress: "127.0.0.1:63099",
		TrustDomain: "test.cluster",
		DataDir:     t.TempDir(),
		Bootstrap: BootstrapOptions{
			SPKIPin:     pin,
			ServerAddr:  serverAddr,
			JoinHTTPURL: srv.URL,
			JoinToken:   token,
		},
	}
	_, err = Join(context.Background(), opts, discardLogger())
	var verifyErr *JoinedLeafVerifyError
	if !errors.As(err, &verifyErr) {
		t.Fatalf("Join with a leaf signed by the wrong CA = %v, want *JoinedLeafVerifyError", err)
	}

	if _, found, _ := loadRole(opts.DataDir); found {
		t.Fatal("a refused join must not persist a role file")
	}
}
