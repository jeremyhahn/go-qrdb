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

//go:build integration

// cluster_ca_anchor_spof_test.go proves the cluster-CA anchor single point
// of failure is closed: before cmd/qrdbd/bootstrap.go's servesAnchor and
// startMemberAnchorAdvertiser, only the node that generated the self-issued
// cluster CA (Key A) ever registered /v1/ca/bootstrap or ran a Noise
// anchor listener. Once that node was gone for good, clusterenroll.Join
// could never reach an anchor or a signer again, for any new node.
//
// This drives a real, 4-real-process self-issued-CA Raft cluster through
// the exact production sequence docs/clustering/architecture.md and cli.md
// describe for growing one: a founder generates Key A, and every
// subsequent node fetches the trust anchor (FetchAnchor) and submits a
// member-side CSR (Join) against an admitted, unsealed member, then is
// admitted to the Raft group with `admin member add`. It then kills the
// founder outright, evicts it from the Raft group through a survivor, and
// grows the cluster by one more real node -- this time through that
// survivor, never the dead founder.
//
// Admin and KV operations against a joined node are driven over raw HTTPS
// rather than the qrdb CLI, deliberately: a member-side CSR join leaf
// (clustertls.IdentityLeafTemplate) carries only a SPIFFE URI SAN, never a
// DNS/IP one -- see docs/clustering/architecture.md, "The leaf carries the
// bound identity only" -- so standard Go TLS hostname verification against
// a bare IP address can never succeed for such a node's own listener, by
// design, on any client. insecureAdminClient still verifies the full
// certificate chain against the node's own cached CA; it skips only the
// hostname check a dial-by-IP test has no SPIFFE identity to satisfy in the
// first place.
package integration_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-truststrap/pkg/spkipin"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
)

// clusterCAAnchorNode is one running qrdbd process in the self-issued
// (non-BYO-TLS) cluster this file's test grows from 1 to 4 real nodes.
type clusterCAAnchorNode struct {
	replicaID uint64
	raftAddr  string
	adminPort int
	dataDir   string
	identity  string // this node's own Raft member identity (spki:<hex>)
	spkiPin   string // SPKI pin of this node's own leaf (node-cert.pem)
	caPath    string // this node's own cached ca.pem
	cmd       *exec.Cmd
	client    *http.Client // insecureAdminClient against this node's own ca.pem
}

// nodeIdentityAndPin reads a node's own leaf certificate off disk and
// derives both its Raft member identity (cluster.MemberIdentityFromPEM) and
// its SPKI pin (spkipin.ComputeSPKIPin) -- everything a peer needs to name
// it as an anchor (--member-identities) or a trust-anchor/join target
// (--bootstrap-spki-pin), without going through that node's admin API at
// all: the certificate is written to disk during clusterenroll.Resolve/
// Join, before the Raft host -- and so before the admin listener -- exists.
func nodeIdentityAndPin(t *testing.T, certPath string) (identity, pin string) {
	t.Helper()
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read node cert %s: %v", certPath, err)
	}
	identity, err = cluster.MemberIdentityFromPEM(certPEM)
	if err != nil {
		t.Fatalf("derive member identity from %s: %v", certPath, err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatalf("no PEM block in %s", certPath)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse leaf certificate %s: %v", certPath, err)
	}
	return identity, spkipin.ComputeSPKIPin(leaf)
}

// writeTokenFile persists token to a fresh, owner-only file for
// --bootstrap-join-token-file.
func writeTokenFile(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "join-token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatalf("write join token file: %v", err)
	}
	return path
}

// generateNodeKeypairAndIdentity generates a fresh ECDSA P-256 keypair and
// derives the Raft member identity it will carry under ANY certificate
// built over it: cluster.MemberIdentityFromCertificate hashes only
// RawSubjectPublicKeyInfo, the DER encoding of the public key alone, which
// x509.MarshalPKIXPublicKey reproduces byte-for-byte without needing a
// certificate to exist yet. This is what lets the identity be computed --
// and handed to a FOUNDING peer's --member-identities -- before the leaf
// that will actually carry it has been signed by anyone.
func generateNodeKeypairAndIdentity(t *testing.T) (key *ecdsa.PrivateKey, keyPEM []byte, identity string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generateNodeKeypairAndIdentity: generate key: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("generateNodeKeypairAndIdentity: marshal key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	spkiDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("generateNodeKeypairAndIdentity: marshal public key: %v", err)
	}
	sum := sha256.Sum256(spkiDER)
	identity = "spki:" + hex.EncodeToString(sum[:])
	return key, keyPEM, identity
}

// signCertWithCA signs template/pub using the cluster CA (Key A) read
// directly from caCertPath/caKeyPath -- node 1's own ca.pem/ca-key.pem,
// readable in the window before confirmClusterCAPersisted deletes the key,
// which cannot happen until a leader is elected: for a 2-member FOUNDING
// group that needs 2-of-2 quorum, impossible until the second founder (the
// eventual caller of everything this signs) is also up.
func signCertWithCA(t *testing.T, caCertPath, caKeyPath string, template *x509.Certificate, pub *ecdsa.PublicKey) []byte {
	t.Helper()
	caCertPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		t.Fatalf("signCertWithCA: read %s: %v", caCertPath, err)
	}
	caKeyPEM, err := os.ReadFile(caKeyPath)
	if err != nil {
		t.Fatalf("signCertWithCA: read %s: %v", caKeyPath, err)
	}
	certBlock, _ := pem.Decode(caCertPEM)
	if certBlock == nil {
		t.Fatalf("signCertWithCA: no PEM block in %s", caCertPath)
	}
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatalf("signCertWithCA: parse CA certificate: %v", err)
	}
	keyBlock, _ := pem.Decode(caKeyPEM)
	if keyBlock == nil {
		t.Fatalf("signCertWithCA: no PEM block in %s", caKeyPath)
	}
	caKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatalf("signCertWithCA: parse CA key: %v", err)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, pub, caKey)
	if err != nil {
		t.Fatalf("signCertWithCA: create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// nodeLeafTemplate builds a founding peer's Raft-transport-and-admin-listener
// leaf template: real hostname/IP SANs, unlike a member-side CSR join's
// identity-only leaf, because this one also has to serve node 2's admin
// REST listener directly (no insecure skip-hostname client needed for it).
func nodeLeafTemplate(cn string) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
}

// adminCertTemplate builds the bootstrap admin credential's template: CN
// must be exactly "admin" (the bootstrap admin's username is fixed) and OU
// must carry the system tenant first, both required by firstboot's own
// validation of a presented admin certificate. Signed with Key A (see
// signCertWithCA) rather than an unrelated throwaway CA, this chains to
// the one CA every node in the cluster already trusts by construction, so
// it passes firstboot's stricter check too -- not only the ceremony's own
// acceptance -- for whichever node happens to WIN the race to create the
// system tenant (ceremonyBringUpBarrierHTTP's throwaway, unrelated-CA
// credential is only safe for a node that is guaranteed to LOSE that race,
// because the tenant it would create already exists by the time it calls
// in; a founder racing for the win has no such guarantee).
func adminCertTemplate() *x509.Certificate {
	return &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano() + 1),
		Subject:      pkix.Name{CommonName: "admin", OrganizationalUnit: []string{"system"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
}

// insecureAdminClient returns an HTTPS client that trusts caPath for CHAIN
// verification but skips hostname checking -- see this file's own doc
// comment for why that is the correct shape here, not a shortcut.
func insecureAdminClient(t *testing.T, caPath string, timeout time.Duration) *http.Client {
	t.Helper()
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("read %s: %v", caPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatalf("no CA certificate found in %s", caPath)
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: pool,
				// Chain verification happens in VerifyConnection below,
				// against the same pool; this only turns off the hostname
				// check crypto/tls would otherwise run first.
				InsecureSkipVerify: true,
				VerifyConnection: func(cs tls.ConnectionState) error {
					if len(cs.PeerCertificates) == 0 {
						return errors.New("no peer certificate presented")
					}
					intermediates := x509.NewCertPool()
					for _, c := range cs.PeerCertificates[1:] {
						intermediates.AddCert(c)
					}
					_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{
						Roots:         pool,
						Intermediates: intermediates,
						KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
					})
					return err
				},
			},
		},
	}
}

// waitForReachable polls the public, unauthenticated /health route (served
// even while sealed) until client can complete a TLS handshake and get a
// response, or timeout elapses.
func waitForReachable(t *testing.T, client *http.Client, adminPort int, timeout time.Duration) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/health", adminPort)
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			return
		}
		lastErr = err
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for 127.0.0.1:%d to accept connections: %v", timeout, adminPort, lastErr)
}

// throwawayAdminCertAndPool generates a throwaway ECDSA P-256 CA and a leaf
// signed by it for cn, needing no connection to the cluster CA at all --
// exactly the shape ceremonyBringUpBarrierHTTP's admin_cert_pem /
// admin_cert_pool_pem (bring-your-own, foreign issuer) path exists for. A
// bare self-signed leaf is refused ("certificate signed by unknown
// authority"): the ceremony verifies the chain against the pool the
// request supplies, so the pool has to be supplied, not merely a cert that
// signed itself.
func throwawayAdminCertAndPool(t *testing.T, cn string) (leafPEM, poolPEM []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("throwawayAdminCertAndPool: generate CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn + "-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("throwawayAdminCertAndPool: create CA certificate: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("throwawayAdminCertAndPool: parse CA certificate: %v", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("throwawayAdminCertAndPool: generate leaf key: %v", err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano() + 1),
		Subject:      pkix.Name{CommonName: cn, OrganizationalUnit: []string{"system"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("throwawayAdminCertAndPool: create leaf certificate: %v", err)
	}
	leafPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	poolPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	return leafPEM, poolPEM
}

// ceremonyBringUpBarrierHTTP drives POST /v1/ceremony/initialize to bring up
// THIS node's own local barrier -- the system barrier refuses
// /v1/barrier/initialize directly ("the system barrier is brought up by
// \"qrdb init\", not \"barrier init\""), so the ceremony endpoint, which
// brings the barrier up itself in the same call (transport.
// CeremonyInitializeRequest's own doc comment), is the only way in. It
// selects the bring-your-own-certificate admin-credential path
// (admin_cert_pem, a throwaway self-signed cert -- never admin_generate):
// generate needs a working CeremonyCA, which in turn needs this node's
// ClusterCAProv to read the replicated cluster CA, and this node cannot do
// that yet -- it is not a Raft member until addMemberHTTP runs, after this
// call returns. The BYO path needs no CA at all. The cluster's real admin
// already exists (node 1's own `qrdb init`); this throwaway credential is
// never used to authenticate anything (auth is disabled for this test) --
// only its presence satisfies the ceremony's "exactly one admin-credential
// path selected" requirement so this node's own barrier comes up. An
// "already" response (the ceremony's one-time state, not this node's own
// barrier) is tolerated exactly as helpers_test.go's initAndUnsealBarrier
// already tolerates it for every founder of an explicit-TLS multi-node
// cluster.
func ceremonyBringUpBarrierHTTP(t *testing.T, client *http.Client, adminPort int, cn string) {
	t.Helper()
	leafPEM, poolPEM := throwawayAdminCertAndPool(t, cn)
	url := fmt.Sprintf("https://127.0.0.1:%d/v1/ceremony/initialize", adminPort)
	reqBody, err := json.Marshal(map[string]any{
		"so_pin":              "SOPIN-" + cn,
		"user_pin":            "USERPIN-" + cn,
		"admin_cert_pem":      string(leafPEM),
		"admin_cert_pool_pem": string(poolPEM),
		"secret":              []byte(""),
	})
	if err != nil {
		t.Fatalf("marshal ceremony/initialize request: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, postErr := client.Post(url, "application/json", bytes.NewReader(reqBody))
		if postErr != nil {
			lastErr = postErr
			time.Sleep(200 * time.Millisecond)
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 300 || bytes.Contains(respBody, []byte("already")) {
			return
		}
		lastErr = fmt.Errorf("status %d, body %s", resp.StatusCode, respBody)
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("POST /v1/ceremony/initialize never succeeded within 15s: %v", lastErr)
}

// ceremonyBringUpBarrierWithCertHTTP is ceremonyBringUpBarrierHTTP's sibling
// for a node that might WIN the race to create the system tenant (node 2,
// the second founder): certPEM must already chain to Key A (adminCertTemplate
// signed via signCertWithCA), not an unrelated throwaway CA, because
// firstboot's OWN validation of a presented admin certificate -- separate
// from, and stricter than, the ceremony's own acceptance of it -- requires
// a trusted chain whenever this call is the one that actually creates the
// tenant and binds the admin, which an unrelated unprivileged joiner
// (ceremonyBringUpBarrierHTTP's throwaway cert) is never in a position to
// be. No admin_cert_pool_pem is passed: certPEM already chains to this
// node's own default trust (Key A, via --tls-ca-file), so there is no
// foreign issuer to vouch for. An "already" response is tolerated exactly
// as ceremonyBringUpBarrierHTTP's is.
func ceremonyBringUpBarrierWithCertHTTP(t *testing.T, client *http.Client, adminPort int, certPEM []byte) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/v1/ceremony/initialize", adminPort)
	reqBody, err := json.Marshal(map[string]any{
		"so_pin":         "SOPIN-node2-founder",
		"user_pin":       "USERPIN-node2-founder",
		"admin_cert_pem": string(certPEM),
		"secret":         []byte(""),
	})
	if err != nil {
		t.Fatalf("marshal ceremony/initialize request: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, postErr := client.Post(url, "application/json", bytes.NewReader(reqBody))
		if postErr != nil {
			lastErr = postErr
			time.Sleep(200 * time.Millisecond)
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 300 || bytes.Contains(respBody, []byte("already")) {
			return
		}
		lastErr = fmt.Errorf("status %d, body %s", resp.StatusCode, respBody)
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("POST /v1/ceremony/initialize never succeeded within 15s: %v", lastErr)
}

// mintJoinTokenHTTP drives POST /v1/cluster/join-tokens and returns the
// minted token. Retried on 503 (NOT_LEADER): minting right after a
// membership change races the Raft group settling on a new leader between
// the survivors.
func mintJoinTokenHTTP(t *testing.T, client *http.Client, adminPort int, nodeID string) string {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/v1/cluster/join-tokens", adminPort)
	reqBody, err := json.Marshal(map[string]any{"node_id": nodeID, "ttl_seconds": 3600})
	if err != nil {
		t.Fatalf("marshal join-tokens request: %v", err)
	}
	deadline := time.Now().Add(90 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, postErr := client.Post(url, "application/json", bytes.NewReader(reqBody))
		if postErr != nil {
			lastErr = postErr
			time.Sleep(300 * time.Millisecond)
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusCreated {
			var out struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal(respBody, &out); err != nil {
				t.Fatalf("decode join-tokens response for %s: %v, body %s", nodeID, err, respBody)
			}
			return out.Token
		}
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("POST /v1/cluster/join-tokens (%s): status %d, body %s", nodeID, resp.StatusCode, respBody)
		}
		lastErr = fmt.Errorf("status %d, body %s", resp.StatusCode, respBody)
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("POST /v1/cluster/join-tokens (%s) never succeeded within 90s: %v", nodeID, lastErr)
	return ""
}

// addMemberHTTP drives POST /membership/add, admitting replicaID at addr
// with identity as a Raft voter of shardID. Retried on 503 (NOT_LEADER):
// the previous member add can itself have just caused a leadership
// transition among the now-larger voter set.
func addMemberHTTP(t *testing.T, client *http.Client, adminPort int, shardID, replicaID uint64, addr, identity string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/membership/add", adminPort)
	reqBody, err := json.Marshal(map[string]any{
		"shard_id": shardID, "replica_id": replicaID, "address": addr, "identity": identity,
	})
	if err != nil {
		t.Fatalf("marshal membership/add request: %v", err)
	}
	deadline := time.Now().Add(90 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, postErr := client.Post(url, "application/json", bytes.NewReader(reqBody))
		if postErr != nil {
			lastErr = postErr
			time.Sleep(300 * time.Millisecond)
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return
		}
		// 503 is the same leadership-transition race every other helper in
		// this file retries on. 409 naming "feature advertisement" is a
		// SEPARATE, also-transient race: the joining node's own pre-
		// membership advertisement (clusterenroll.FeatureAdvertiser.
		// AdvertiseJoiner, published by the SIGNING member as part of the
		// join handshake -- see docs/clustering/engineering.md, "Joining a
		// new node after activation") can take a moment to become visible
		// to the gate this call consults, or the joiner's own gate may
		// still be mid-boot. 508 is a THIRD transient race, seen right
		// after a 2-member group has just founded: the replica-forwarding
		// registry this call's target consults to find the current leader
		// has not yet converged, so a request can bounce leader-forward
		// back to the node that sent it before settling. Any OTHER 409 is
		// a real, permanent refusal and still fails immediately.
		retryable := resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusLoopDetected ||
			(resp.StatusCode == http.StatusConflict && bytes.Contains(respBody, []byte("feature advertisement")))
		if !retryable {
			t.Fatalf("POST /membership/add %s (replica %d): status %d, body %s", addr, replicaID, resp.StatusCode, respBody)
		}
		lastErr = fmt.Errorf("status %d, body %s", resp.StatusCode, respBody)
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("POST /membership/add %s (replica %d) never succeeded within 90s: %v", addr, replicaID, lastErr)
}

// removeMemberHTTP drives POST /membership/remove and returns the raw
// status/body so the caller can retry while the surviving members settle
// on a leader.
func removeMemberHTTP(t *testing.T, client *http.Client, adminPort int, shardID, replicaID uint64) (int, string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/membership/remove", adminPort)
	reqBody, err := json.Marshal(map[string]any{"shard_id": shardID, "replica_id": replicaID})
	if err != nil {
		t.Fatalf("marshal membership/remove request: %v", err)
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(respBody)
}

// kvPutHTTP drives PUT /kv/{key}. Retried on 503 (NOT_LEADER): the same
// leadership-transition race addMemberHTTP's own comment describes.
func kvPutHTTP(t *testing.T, client *http.Client, adminPort int, key string, value []byte) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", adminPort, key)
	deadline := time.Now().Add(90 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(value))
		if err != nil {
			t.Fatalf("kvPutHTTP: new request: %v", err)
		}
		resp, doErr := client.Do(req)
		if doErr != nil {
			lastErr = doErr
			time.Sleep(300 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusNoContent {
			return
		}
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("kvPutHTTP %s: status %d, body %s", key, resp.StatusCode, body)
		}
		lastErr = fmt.Errorf("status %d, body %s", resp.StatusCode, body)
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("kvPutHTTP %s never succeeded within 90s: %v", key, lastErr)
}

// waitForKVValueHTTP polls GET /kv/{key} until it reports want or timeout
// elapses.
func waitForKVValueHTTP(t *testing.T, client *http.Client, adminPort int, key, want string, timeout time.Duration) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", adminPort, key)
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(300 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && string(body) == want {
			return
		}
		lastErr = fmt.Errorf("status %d, body %s", resp.StatusCode, body)
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for kv %s = %q: %v", timeout, key, want, lastErr)
}

// startFoundingSelfIssuedCANode starts node 1, the FIRST and ONLY
// self-issued founder: no --tls-* and no --bootstrap-* flags, so
// clusterenroll.Resolve's path 5 generates the self-issued cluster CA
// (Key A) and this node's own leaf. peers/memberIDs name BOTH founders
// (node 1 and node 2) up front -- a 2-member --peers list is what keeps
// cluster.IsSingleNode false and so keeps cmd/qrdbd from building this
// host quicraft.WithoutTransport(), the single-node optimization that
// would otherwise make it permanently unable to accept a second Raft
// peer no matter what `admin member add` is run against it later. Returns
// before the barrier is brought up: the caller must read this node's CA
// material to sign node 2's leaf, start node 2, and only THEN bring up
// node 1's own barrier -- a REPLICATED write, impossible before node 2
// also exists and the pair can elect a leader (2-of-2 quorum).
// writeNodeConfigYAML writes a config combining the seal config every node
// in this test uses with auth.peer_admin_urls naming every OTHER node's real
// admin URL. Required because (*peerAdminRegistry).ResolveAdminURL's
// fallback -- used when a raft address has no explicit Register() entry --
// derives scheme://<host-from-raftAddr>:<THIS node's own admin port>, which
// assumes the one-node-per-host production topology (same admin port,
// different host); every node in this test instead shares 127.0.0.1 with a
// DIFFERENT admin port, so without this config a node forwarding a
// not-leader write resolves straight back to its OWN admin URL, never the
// actual leader's. test/integration/cluster_test.go's
// writePeerAdminURLConfig establishes the identical pattern for a
// statically-founded cluster; this is its equivalent for nodes some of
// which are not yet founded when this is written (their entries are
// pre-computed from already-allocated ports).
func writeNodeConfigYAML(t *testing.T, peerAdminURLs map[string]string) string {
	t.Helper()
	lines := "  peer_admin_urls:\n"
	for raftAddr, adminURL := range peerAdminURLs {
		lines += fmt.Sprintf("    - raft_addr: %q\n      admin_url: %q\n", raftAddr, adminURL)
	}
	yaml := "seal:\n  enabled: true\n  strategy: none\nauth:\n" + lines
	return writeSealYAML(t, yaml)
}

func startFoundingSelfIssuedCANode(t *testing.T, raftPort, adminPort, grpcPort, noisePort, joinPort int, peers, memberIDs string, peerAdminURLs map[string]string) *clusterCAAnchorNode {
	t.Helper()
	bin := buildBinary(t)
	dataDir := t.TempDir()
	cfgPath := writeNodeConfigYAML(t, peerAdminURLs)
	raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPort)

	args := []string{
		"start",
		"--config", cfgPath,
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPort),
		"--raft-address", raftAddr,
		"--data-dir", dataDir,
		"--sm", "mvcc",
		"--discovery", "static",
		"--peers", peers,
		"--member-identities", memberIDs,
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--auth-enabled=false",
		"--enable-rest",
		"--cluster-join-enabled",
		"--bootstrap-noise-port", fmt.Sprintf("%d", noisePort),
		"--bootstrap-noise-host", "127.0.0.1",
		"--cluster-join-listen-port", fmt.Sprintf("%d", joinPort),
		"--cluster-join-listen-host", "127.0.0.1",
	}
	cmd := startNode(t, bin, args, "[node1] ")

	certPath := filepath.Join(dataDir, "node-cert.pem")
	caPath := filepath.Join(dataDir, "ca.pem")
	caKeyPath := filepath.Join(dataDir, "ca-key.pem")
	waitForFile(t, certPath, 30*time.Second)
	waitForFile(t, caPath, 30*time.Second)
	waitForFile(t, caKeyPath, 30*time.Second)

	identity, pin := nodeIdentityAndPin(t, certPath)
	client := insecureAdminClient(t, caPath, 20*time.Second)
	waitForReachable(t, client, adminPort, 30*time.Second)

	return &clusterCAAnchorNode{
		replicaID: 1,
		raftAddr:  raftAddr,
		adminPort: adminPort,
		dataDir:   dataDir,
		identity:  identity,
		spkiPin:   pin,
		caPath:    caPath,
		cmd:       cmd,
		client:    client,
	}
}

// startSecondFounderExplicitTLS starts node 2, the SECOND founder of the
// 2-member group, with an EXPLICIT leaf signed by node 1's own cluster CA
// key (signCertWithCA with nodeLeafTemplate). This node is deliberately BYO-TLS
// (TLSBundle.ExplicitTLS) from cmd/qrdbd's perspective: it holds no
// ClusterCAProv and so cannot itself serve the trust anchor to a later
// joiner -- servesAnchor's doc comment covers exactly why a node that
// generated or joined the cluster CA through clusterenroll CAN serve it
// and a node handed an externally-signed leaf cannot. Node 3, admitted
// later through the ordinary member-side CSR flow, is this test's actual
// surviving anchor server; node 2 only exists so the group can found with
// a real 2-member Raft transport (see startFoundingSelfIssuedCANode).
func startSecondFounderExplicitTLS(t *testing.T, raftPort, adminPort, grpcPort int, peers, memberIDs, caCertPath string, leafPEM, keyPEM []byte, peerAdminURLs map[string]string) *clusterCAAnchorNode {
	t.Helper()
	bin := buildBinary(t)
	dataDir := t.TempDir()
	cfgPath := writeNodeConfigYAML(t, peerAdminURLs)
	raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPort)

	certDir := t.TempDir()
	certPath := filepath.Join(certDir, "node-cert.pem")
	keyPath := filepath.Join(certDir, "node-key.pem")
	if err := os.WriteFile(certPath, leafPEM, 0o600); err != nil {
		t.Fatalf("write %s: %v", certPath, err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write %s: %v", keyPath, err)
	}

	args := []string{
		"start",
		"--config", cfgPath,
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPort),
		"--raft-address", raftAddr,
		"--data-dir", dataDir,
		"--sm", "mvcc",
		"--discovery", "static",
		"--peers", peers,
		"--member-identities", memberIDs,
		"--shard-id", "1",
		"--replica-id", "2",
		"--admin-port", fmt.Sprintf("%d", adminPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--auth-enabled=false",
		"--enable-rest",
		"--tls-ca-file", caCertPath,
		"--tls-cert-file", certPath,
		"--tls-key-file", keyPath,
	}
	cmd := startNode(t, bin, args, "[node2] ")

	identity, pin := nodeIdentityAndPin(t, certPath)
	client := insecureAdminClient(t, caCertPath, 20*time.Second)
	waitForReachable(t, client, adminPort, 30*time.Second)

	return &clusterCAAnchorNode{
		replicaID: 2,
		raftAddr:  raftAddr,
		adminPort: adminPort,
		dataDir:   dataDir,
		identity:  identity,
		spkiPin:   pin,
		caPath:    caCertPath,
		cmd:       cmd,
		client:    client,
	}
}

// startJoiningSelfIssuedCANode starts a brand-new qrdbd process that
// fetches the cluster's trust anchor and a signed leaf through anchor (its
// own self-issued cluster CA), then attaches to shard 1 as a Raft joiner
// anchored on the members named in memberIDs. The caller still has to admit
// it with addMemberHTTP before it can serve: --join only tells go-quicraft
// this is not a founding call.
func startJoiningSelfIssuedCANode(
	t *testing.T,
	replicaID uint64,
	raftPort, adminPort, grpcPort, noisePort, joinPort int,
	peers, memberIDs string,
	anchor *clusterCAAnchorNode,
	token string,
	peerAdminURLs map[string]string,
) *clusterCAAnchorNode {
	t.Helper()
	bin := buildBinary(t)
	dataDir := t.TempDir()
	cfgPath := writeNodeConfigYAML(t, peerAdminURLs)
	raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPort)
	tokenFile := writeTokenFile(t, token)

	args := []string{
		"start",
		"--config", cfgPath,
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPort),
		"--raft-address", raftAddr,
		"--data-dir", dataDir,
		"--sm", "mvcc",
		"--discovery", "static",
		"--peers", peers,
		"--member-identities", memberIDs,
		"--join",
		"--shard-id", "1",
		"--replica-id", fmt.Sprintf("%d", replicaID),
		"--admin-port", fmt.Sprintf("%d", adminPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--auth-enabled=false",
		"--enable-rest",
		"--cluster-join-enabled",
		"--bootstrap-noise-port", fmt.Sprintf("%d", noisePort),
		"--bootstrap-noise-host", "127.0.0.1",
		"--cluster-join-listen-port", fmt.Sprintf("%d", joinPort),
		"--cluster-join-listen-host", "127.0.0.1",
		"--bootstrap-spki-pin", anchor.spkiPin,
		"--bootstrap-addr", fmt.Sprintf("127.0.0.1:%d", anchor.adminPort),
		"--bootstrap-join-url", fmt.Sprintf("https://127.0.0.1:%d", anchor.adminPort),
		"--bootstrap-join-token-file", tokenFile,
	}
	cmd := startNode(t, bin, args, fmt.Sprintf("[node%d] ", replicaID))

	certPath := filepath.Join(dataDir, "node-cert.pem")
	caPath := filepath.Join(dataDir, "ca.pem")
	// This is the proof that matters: FetchAnchor (against anchor's
	// /v1/ca/bootstrap) and the CSR exchange (against anchor's
	// /v1/enroll/join) both completed and the leaf was persisted, all
	// BEFORE this replica is ever admitted to the Raft group.
	waitForFile(t, certPath, 30*time.Second)
	waitForFile(t, caPath, 30*time.Second)

	identity, pin := nodeIdentityAndPin(t, certPath)

	// Deliberately returns here, with no client built and no barrier
	// brought up yet: this replica's own qrdbd process, as soon as it has
	// written the files above, immediately starts campaigning for votes
	// as the replica ID it was given (go-quicraft catches it up on the
	// committed membership before letting it campaign) -- but node 1/node
	// 2 reject every request from it as "unknown_member" until
	// addMemberHTTP actually admits it, and qrdbd's own internal
	// wait-for-leader step (a fixed ~14s budget baked into the binary,
	// not configurable) gives up and the WHOLE PROCESS EXITS if no leader
	// is visible to IT by then. The caller must call addMemberHTTP
	// immediately against this return value -- before doing anything
	// else, including reaching this node's own admin port -- and only
	// then call finishJoiningNode.
	return &clusterCAAnchorNode{
		replicaID: replicaID,
		raftAddr:  raftAddr,
		adminPort: adminPort,
		dataDir:   dataDir,
		identity:  identity,
		spkiPin:   pin,
		caPath:    caPath,
		cmd:       cmd,
	}
}

// finishJoiningNode completes a joining node's setup after addMemberHTTP has
// already admitted it (see startJoiningSelfIssuedCANode's doc comment for
// why the ordering matters): builds its admin client, waits for the admin
// port to accept connections, and brings up its own local barrier.
func finishJoiningNode(t *testing.T, node *clusterCAAnchorNode) {
	t.Helper()
	node.client = insecureAdminClient(t, node.caPath, 20*time.Second)
	waitForReachable(t, node.client, node.adminPort, 30*time.Second)

	// This node's own barrier has never been brought up: every node, not
	// only the founder, needs its own per-node barrier bring-up (see
	// ceremonyBringUpBarrierHTTP's doc comment) -- the system tenant, roles
	// and admin already exist in the replicated store it is about to catch
	// up on. CommonName must be exactly "admin": the bootstrap admin's
	// username is fixed. That account already exists cluster-wide (node
	// 1's own `qrdb init`), so this call's admin-creation half is expected
	// to find it already there -- ceremonyBringUpBarrierHTTP's "already"
	// tolerance -- while its barrier-bring-up half still runs for this
	// node.
	ceremonyBringUpBarrierHTTP(t, node.client, node.adminPort, "admin")
}

// TestCluster_JoinThroughSurvivingMemberAfterCAGeneratorRemoved is the
// end-to-end proof for the cluster-CA anchor single point of failure fix.
//
// Phase 1 founds a real 3-member self-issued-CA Raft group. A self-issued
// CA cluster can only ever FOUND with EXPLICITLY-KNOWN member identities
// (go-quicraft refuses a founding StartShard that omits one), which rules
// out a single self-issued founder growing one member at a time by itself
// -- cmd/qrdbd builds a single-peer host quicraft.WithoutTransport(),
// permanently, so it could never even accept a second peer's connection.
// Node 1 and node 2 therefore found TOGETHER: node 1 generates Key A, and
// node 2's leaf is signed with that same key read directly off node 1's
// disk (signLeafWithCA) -- not a second copy of the CA, the IDENTICAL
// key, read in the one window it is guaranteed to still be there (before
// a leader is elected, impossible before node 2 exists). Node 3 then
// joins through the ordinary, unprivileged member-side CSR flow
// (clusterenroll.FetchAnchor/Join against node 1's /v1/ca/bootstrap and
// /v1/enroll/join) and is admitted as a Raft voter. 3 members means
// losing 1 still leaves a quorum of 2.
//
// Phase 2 kills node 1's process outright and evicts it from the Raft
// group through node 3, a survivor.
//
// Phase 3 is the actual regression proof: node 4, a brand-new node, fetches
// the anchor and submits its CSR THROUGH NODE 3 -- a surviving member, and
// specifically NOT the dead node 1 -- is admitted, catches up on the
// pre-removal data, and accepts a new write that is durable and visible
// from the other survivor. Before cmd/qrdbd/bootstrap.go's servesAnchor and
// startMemberAnchorAdvertiser, node 3 never registered /v1/ca/bootstrap or
// ran a Noise anchor listener, and this phase could not succeed at all --
// only node 1 ever could, and node 1 is gone for good. (Node 2, the
// explicit-TLS second founder, could never have served this role either:
// it holds no ClusterCAProv, by the same servesAnchor gate, which is the
// point -- an externally-signed founder is not a substitute for a real
// admitted member.)
func TestCluster_JoinThroughSurvivingMemberAfterCAGeneratorRemoved(t *testing.T) {
	raftPorts := allocatePorts(t, 4)
	adminPorts := allocateTCPPorts(t, 4)
	grpcPorts := allocateTCPPorts(t, 4)
	noisePorts := allocateTCPPorts(t, 4)
	joinPorts := allocateTCPPorts(t, 4)

	node1Addr := fmt.Sprintf("127.0.0.1:%d", raftPorts[0])
	node2Addr := fmt.Sprintf("127.0.0.1:%d", raftPorts[1])
	node3Addr := fmt.Sprintf("127.0.0.1:%d", raftPorts[2])
	node4Addr := fmt.Sprintf("127.0.0.1:%d", raftPorts[3])
	node2Key, node2KeyPEM, node2Identity := generateNodeKeypairAndIdentity(t)
	peers12 := fmt.Sprintf("1=%s,2=%s", node1Addr, node2Addr)

	// Every node gets every OTHER node's real admin URL up front: see
	// writeNodeConfigYAML's doc comment for why ResolveAdminURL's fallback
	// (same admin port, different host -- the one-node-per-host production
	// assumption) cannot work for this test's four nodes, which all share
	// 127.0.0.1 with different ports.
	peerAdminURLs := map[string]string{
		node1Addr: fmt.Sprintf("https://127.0.0.1:%d", adminPorts[0]),
		node2Addr: fmt.Sprintf("https://127.0.0.1:%d", adminPorts[1]),
		node3Addr: fmt.Sprintf("https://127.0.0.1:%d", adminPorts[2]),
		node4Addr: fmt.Sprintf("https://127.0.0.1:%d", adminPorts[3]),
	}

	// Node 1 founds, generating Key A; returns before its own barrier is
	// brought up (see startFoundingSelfIssuedCANode's doc comment).
	node1 := startFoundingSelfIssuedCANode(t, raftPorts[0], adminPorts[0], grpcPorts[0], noisePorts[0], joinPorts[0],
		peers12, fmt.Sprintf("2=%s", node2Identity), peerAdminURLs)

	// Sign node 2's leaf AND its bootstrap admin credential with node 1's
	// cluster CA key while it is still on disk -- both, in this same early
	// window, since confirmClusterCAPersisted may delete it the moment a
	// leader is elected, which can happen as soon as node 2 starts below.
	node1CAKeyPath := filepath.Join(node1.dataDir, "ca-key.pem")
	node2LeafPEM := signCertWithCA(t, node1.caPath, node1CAKeyPath, nodeLeafTemplate("node2"), &node2Key.PublicKey)
	node2AdminKey, _, _ := generateNodeKeypairAndIdentity(t)
	node2AdminCertPEM := signCertWithCA(t, node1.caPath, node1CAKeyPath, adminCertTemplate(), &node2AdminKey.PublicKey)
	node2 := startSecondFounderExplicitTLS(t, raftPorts[1], adminPorts[1], grpcPorts[1],
		peers12, fmt.Sprintf("1=%s", node1.identity), node1.caPath, node2LeafPEM, node2KeyPEM, peerAdminURLs)
	if node2.identity != node2Identity {
		t.Fatalf("node 2 identity mismatch: precomputed %s, derived from signed leaf %s", node2Identity, node2.identity)
	}

	// Both founders are up: now a leader can be elected (2-of-2 quorum),
	// so each node's own barrier bring-up -- a replicated write -- can
	// finally succeed. Both ceremony calls present a credential that
	// chains to Key A (node 1's admin-generate path mints one that does by
	// construction; node 2's BYO credential is the one just signed above),
	// so whichever of the two WINS the race to create the system tenant
	// and admin passes firstboot's stricter validation regardless -- see
	// ceremonyBringUpBarrierWithCertHTTP's doc comment for why that
	// matters and ceremonyBringUpBarrierHTTP's (unrelated-CA, throwaway)
	// cert would not have been safe here. Whichever loses the race gets
	// "already", tolerated by both helpers identically.
	cli := buildCLIBinary(t)
	credDir := t.TempDir()
	bootAdminIdentity(t, cli, node1.caPath, node1.adminPort, "SOPIN-node1-anchor", "USERPIN-node1-anchor",
		filepath.Join(credDir, "admin-cert.pem"), filepath.Join(credDir, "admin-key.pem"), "",
		"--barrier-strategy", "none")
	ceremonyBringUpBarrierWithCertHTTP(t, node2.client, node2.adminPort, node2AdminCertPEM)

	waitForHealthy(t, node1.dataDir, 90*time.Second)
	waitForHealthy(t, node2.dataDir, 90*time.Second)
	waitForReachable(t, node1.client, node1.adminPort, 30*time.Second)
	waitForReachable(t, node2.client, node2.adminPort, 30*time.Second)

	// Node 3 joins THROUGH node 1, over the ordinary member-side CSR flow:
	// a genuine 3-member group, so losing 1 of them later still leaves a
	// quorum of 2.
	//
	// The join token is minted for node 3's own raft address, never a
	// cosmetic label: MintJoinToken binds the token to a SPIFFE identity
	// built from exactly the string passed as nodeID
	// (spiffe.NodeIdentity{TrustDomain, NodeID: nodeID}.String()), and
	// issueAndAdmit derives the pre-membership feature advertisement's
	// Address from that SAME bound identity. featuregate.Gate.Supports
	// matches an advertisement to a replica by exact address-string
	// equality, so a label here would advertise an address nothing ever
	// dials, and /membership/add would refuse forever with "no live
	// feature advertisement" -- indistinguishable from a genuine
	// propagation bug, but actually just a token minted for the wrong
	// name.
	token3 := mintJoinTokenHTTP(t, node1.client, node1.adminPort, node3Addr)
	peers123 := fmt.Sprintf("%s,3=%s", peers12, node3Addr)
	memberIDs13 := fmt.Sprintf("1=%s,2=%s", node1.identity, node2.identity)
	node3 := startJoiningSelfIssuedCANode(t, 3, raftPorts[2], adminPorts[2], grpcPorts[2], noisePorts[2], joinPorts[2],
		peers123, memberIDs13, node1, token3, peerAdminURLs)
	// addMemberHTTP must run BEFORE anything else touches node 3 -- see
	// startJoiningSelfIssuedCANode's doc comment: its own qrdbd process
	// starts campaigning as replica 3 the moment its files above exist,
	// and gives up (exiting outright) if no leader becomes visible to it
	// within its own fixed ~14s internal budget, which only admission
	// unblocks.
	addMemberHTTP(t, node1.client, node1.adminPort, 1, 3, node3.raftAddr, node3.identity)
	finishJoiningNode(t, node3)
	waitForHealthy(t, node3.dataDir, 60*time.Second)

	// Seed data before removing node 1, so its survival through the
	// removal and the later join proves real replication, not luck.
	kvPutHTTP(t, node1.client, node1.adminPort, "before-removal", []byte("v1"))
	waitForKVValueHTTP(t, node2.client, node2.adminPort, "before-removal", "v1", 30*time.Second)
	waitForKVValueHTTP(t, node3.client, node3.adminPort, "before-removal", "v1", 30*time.Second)

	// Permanently remove the CA-generating node: kill its process outright,
	// then evict it from the Raft group through survivor node 3. The
	// remaining 2 of the original 3 members still hold quorum, so this
	// (and everything from here on) is retried rather than assumed
	// immediate: the pair needs a moment to notice node 1 is gone and
	// settle on a leader between themselves.
	if node1.cmd.Process == nil {
		t.Fatal("node 1 has no process to kill")
	}
	killNode(t, node1.cmd)

	removeDeadline := time.Now().Add(90 * time.Second)
	var removeCode int
	var removeBody string
	for time.Now().Before(removeDeadline) {
		removeCode, removeBody = removeMemberHTTP(t, node3.client, node3.adminPort, 1, 1)
		if removeCode == http.StatusOK {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if removeCode != http.StatusOK {
		t.Fatalf("POST /membership/remove 1 via surviving node 3: status %d, body %s", removeCode, removeBody)
	}

	// Node 4 is a brand-new node that fetches the anchor and submits its
	// CSR THROUGH NODE 3 -- never node 1, which is dead, and never node 2,
	// which never could. This is the behavior that did not exist before
	// this change.
	//
	// Same reason node 3's token above is minted for its raft address, not
	// a cosmetic label.
	token4 := mintJoinTokenHTTP(t, node3.client, node3.adminPort, node4Addr)
	peers4 := fmt.Sprintf("2=%s,3=%s,4=%s", node2.raftAddr, node3.raftAddr, node4Addr)
	memberIDs4 := fmt.Sprintf("2=%s,3=%s", node2.identity, node3.identity)
	node4 := startJoiningSelfIssuedCANode(t, 4, raftPorts[3], adminPorts[3], grpcPorts[3], noisePorts[3], joinPorts[3],
		peers4, memberIDs4, node3, token4, peerAdminURLs)
	// Same ordering constraint as node 3's own join above.
	addMemberHTTP(t, node3.client, node3.adminPort, 1, 4, node4.raftAddr, node4.identity)
	finishJoiningNode(t, node4)
	waitForHealthy(t, node4.dataDir, 60*time.Second)

	// The new node must join AND serve: it caught up on the pre-removal
	// data over Raft, and a write issued directly against it is durable
	// and visible from both surviving founders.
	waitForKVValueHTTP(t, node4.client, node4.adminPort, "before-removal", "v1", 30*time.Second)
	kvPutHTTP(t, node4.client, node4.adminPort, "after-rejoin", []byte("v2"))
	waitForKVValueHTTP(t, node2.client, node2.adminPort, "after-rejoin", "v2", 30*time.Second)
	waitForKVValueHTTP(t, node3.client, node3.adminPort, "after-rejoin", "v2", 30*time.Second)
}
