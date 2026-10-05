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

package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// stubClusterCAProvider is a minimal cakey.Provider double.
type stubClusterCAProvider struct {
	certErr error
}

func (s *stubClusterCAProvider) Signer(context.Context) (crypto.Signer, error) { return nil, nil }

func (s *stubClusterCAProvider) Certificate(context.Context) (*x509.Certificate, error) {
	if s.certErr != nil {
		return nil, s.certErr
	}
	return &x509.Certificate{}, nil
}

func TestConfirmClusterCAPersisted_DeletesLocalKeyOnSuccess(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca-key.pem")
	if err := os.WriteFile(keyPath, []byte("fake key material"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := confirmClusterCAPersisted(context.Background(), &stubClusterCAProvider{}, dir, true); err != nil {
		t.Fatalf("confirmClusterCAPersisted: %v", err)
	}

	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("ca-key.pem still exists after confirmed persistence (stat err = %v)", err)
	}
}

func TestConfirmClusterCAPersisted_NoLocalKeyToDelete(t *testing.T) {
	dir := t.TempDir()
	// hadLocalKey=false: nothing to delete, no file present either. Must
	// not error just because there is nothing to remove.
	if err := confirmClusterCAPersisted(context.Background(), &stubClusterCAProvider{}, dir, false); err != nil {
		t.Fatalf("confirmClusterCAPersisted: %v", err)
	}
}

func TestConfirmClusterCAPersisted_ProviderFailureIsFatal(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca-key.pem")
	if err := os.WriteFile(keyPath, []byte("fake key material"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	boom := errors.New("store unreachable")
	err := confirmClusterCAPersisted(context.Background(), &stubClusterCAProvider{certErr: boom}, dir, true)
	var persistErr *ClusterCAPersistError
	if !errors.As(err, &persistErr) {
		t.Fatalf("confirmClusterCAPersisted = %v, want *ClusterCAPersistError", err)
	}

	// The local key must NOT be deleted when persistence could not be
	// confirmed -- it is the only remaining copy of the key.
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("ca-key.pem was removed despite a failed persistence check: %v", err)
	}
}

func TestConfirmClusterCAPersisted_DeletionFailureIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	// hadLocalKey=true but no file present (e.g. removed out-of-band
	// between boots): deletion sees ErrNotExist and must not surface it.
	if err := confirmClusterCAPersisted(context.Background(), &stubClusterCAProvider{}, dir, true); err != nil {
		t.Fatalf("confirmClusterCAPersisted: %v", err)
	}
}

// selfSignedTestLeaf builds a minimal self-signed ECDSA P-256 certificate,
// PEM-encoded, carrying nodeURI as its one URI SAN -- or no URI SAN at all
// when nodeURI is "", standing in for a BYO-TLS certificate issued outside
// the cluster CA.
func selfSignedTestLeaf(t *testing.T, nodeURI string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "qrdb-node"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	if nodeURI != "" {
		u, err := url.Parse(nodeURI)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", nodeURI, err)
		}
		tmpl.URIs = []*url.URL{u}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestRecordOwnJoinIdentity_FounderRefusesSubsequentMint is the coordinator-
// required proof: a founder's own leaf (the certificate confirmClusterCAPersisted's
// caller already holds at boot) gets its SPIFFE node identity recorded, and
// MintJoinToken for that SAME node ID is refused afterward -- "existing
// member" now means "the cluster CA has issued this node identity",
// covering a founder that never went through the token path at all.
func TestRecordOwnJoinIdentity_FounderRefusesSubsequentMint(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	const trustDomain = "test.cluster"
	const nodeID = "founder-1"
	certPEM := selfSignedTestLeaf(t, spiffe.NodeURI(trustDomain, nodeID))

	if err := recordOwnJoinIdentity(context.Background(), store, certPEM, trustDomain); err != nil {
		t.Fatalf("recordOwnJoinIdentity: %v", err)
	}

	_, _, err := clusterenroll.MintJoinToken(context.Background(), store, trustDomain, nodeID, time.Hour)
	var existing *clusterenroll.ExistingMemberError
	if !errors.As(err, &existing) {
		t.Fatalf("MintJoinToken for a founder's ID = %v (%T), want *clusterenroll.ExistingMemberError", err, err)
	}
}

// TestRecordOwnJoinIdentity_IdempotentAcrossBoots proves the call is safe
// on every boot, not only the first: recordOwnJoinIdentity is called
// unconditionally after confirmClusterCAPersisted on every start, and a
// restart presenting the SAME leaf must not error just because the
// record already exists (PutIfAbsent's ordinary "already there" outcome).
func TestRecordOwnJoinIdentity_IdempotentAcrossBoots(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	const trustDomain = "test.cluster"
	certPEM := selfSignedTestLeaf(t, spiffe.NodeURI(trustDomain, "founder-1"))

	if err := recordOwnJoinIdentity(context.Background(), store, certPEM, trustDomain); err != nil {
		t.Fatalf("recordOwnJoinIdentity (first boot): %v", err)
	}
	if err := recordOwnJoinIdentity(context.Background(), store, certPEM, trustDomain); err != nil {
		t.Fatalf("recordOwnJoinIdentity (restart, same leaf) = %v, want nil", err)
	}
}

// TestRecordOwnJoinIdentity_FreshIDStillMintable proves recording one
// node's identity does not block minting a token for a DIFFERENT, fresh
// node ID.
func TestRecordOwnJoinIdentity_FreshIDStillMintable(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	const trustDomain = "test.cluster"
	certPEM := selfSignedTestLeaf(t, spiffe.NodeURI(trustDomain, "founder-1"))

	if err := recordOwnJoinIdentity(context.Background(), store, certPEM, trustDomain); err != nil {
		t.Fatalf("recordOwnJoinIdentity: %v", err)
	}

	token, _, err := clusterenroll.MintJoinToken(context.Background(), store, trustDomain, "fresh-node", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken for a fresh node ID = %v, want success", err)
	}
	if token == "" {
		t.Fatal("MintJoinToken returned an empty token")
	}
}

// TestRecordOwnJoinIdentity_NoNodeSANIsNoOp documents the BYO-TLS gap: a
// certificate with no node SPIFFE URI SAN at all (operator-supplied
// --tls-ca/--tls-cert/--tls-key, signed outside the cluster CA) has no
// identity for this function to record. It must not error -- there was
// never an identity to admit -- and must leave the ledger untouched.
func TestRecordOwnJoinIdentity_NoNodeSANIsNoOp(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	certPEM := selfSignedTestLeaf(t, "")

	if err := recordOwnJoinIdentity(context.Background(), store, certPEM, "test.cluster"); err != nil {
		t.Fatalf("recordOwnJoinIdentity (no SAN) = %v, want nil", err)
	}
	keys, err := store.List(context.Background(), "__sys:cluster_join_identities/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected no identity recorded for a cert with no node SPIFFE SAN, got %v", keys)
	}
}

// TestRecordOwnJoinIdentity_MalformedCertIsNoOp proves a certificate that
// fails to decode or parse is treated the same as "no identity to
// record" -- confirmClusterCAPersisted already validated this node's own
// certificate through the cluster CA provider before this function runs,
// so a malformed certPEM here would mean a caller bug, not an admission
// decision; it must not block boot.
func TestRecordOwnJoinIdentity_MalformedCertIsNoOp(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	if err := recordOwnJoinIdentity(context.Background(), store, []byte("not a certificate"), "test.cluster"); err != nil {
		t.Fatalf("recordOwnJoinIdentity (malformed cert) = %v, want nil", err)
	}
}

// TestClusterCABootstrapPEMs_WithholdForExplicitTLS is the BYO-TLS proof:
// bundle.CACertPEM is the operator's own trust anchor for an ExplicitTLS
// bundle, and must never be offered to the built-in cluster CA provider as
// adopt material.
func TestClusterCABootstrapPEMs_WithholdForExplicitTLS(t *testing.T) {
	bundle := &TLSBundle{
		CACertPEM:   []byte("operator ca cert"),
		CAKeyPEM:    []byte("would never actually be set for this path"),
		ExplicitTLS: true,
	}
	if got := clusterCABootstrapCertPEM(bundle); got != nil {
		t.Fatalf("clusterCABootstrapCertPEM(ExplicitTLS) = %q, want nil", got)
	}
	if got := clusterCABootstrapKeyPEM(bundle); got != nil {
		t.Fatalf("clusterCABootstrapKeyPEM(ExplicitTLS) = %q, want nil", got)
	}
}

// TestClusterCABootstrapPEMs_PassThroughForSelfIssued covers every other
// path (cached data-dir material, single-node, first-node, joining node):
// this node's own material must still reach the cluster CA provider.
func TestClusterCABootstrapPEMs_PassThroughForSelfIssued(t *testing.T) {
	bundle := &TLSBundle{
		CACertPEM: []byte("self-issued ca cert"),
		CAKeyPEM:  []byte("self-issued ca key"),
	}
	if got := clusterCABootstrapCertPEM(bundle); string(got) != string(bundle.CACertPEM) {
		t.Fatalf("clusterCABootstrapCertPEM(self-issued) = %q, want %q", got, bundle.CACertPEM)
	}
	if got := clusterCABootstrapKeyPEM(bundle); string(got) != string(bundle.CAKeyPEM) {
		t.Fatalf("clusterCABootstrapKeyPEM(self-issued) = %q, want %q", got, bundle.CAKeyPEM)
	}
}

// stubFlakyClusterCAProvider answers *service.NotLeaderError for the first
// notLeaderUntil-1 calls (or forever, when notLeaderUntil <= 0), then
// succeeds -- it drives awaitClusterCACertificate's transient-retry path
// without a real Raft cluster.
type stubFlakyClusterCAProvider struct {
	mu             sync.Mutex
	calls          int
	notLeaderUntil int
	otherErr       error
}

func (s *stubFlakyClusterCAProvider) Signer(context.Context) (crypto.Signer, error) { return nil, nil }

func (s *stubFlakyClusterCAProvider) Certificate(context.Context) (*x509.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.otherErr != nil {
		return nil, s.otherErr
	}
	if s.notLeaderUntil <= 0 || s.calls < s.notLeaderUntil {
		return nil, &service.NotLeaderError{ShardID: 1}
	}
	return &x509.Certificate{}, nil
}

func (s *stubFlakyClusterCAProvider) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestAwaitClusterCACertificate_SucceedsImmediately proves the common case
// -- no transient error at all -- makes exactly one call.
func TestAwaitClusterCACertificate_SucceedsImmediately(t *testing.T) {
	stub := &stubFlakyClusterCAProvider{notLeaderUntil: 1}
	cert, err := awaitClusterCACertificate(context.Background(), stub, time.Second, time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("awaitClusterCACertificate: %v", err)
	}
	if cert == nil {
		t.Fatal("expected a non-nil certificate")
	}
	if got := stub.callCount(); got != 1 {
		t.Fatalf("Certificate called %d times, want 1 (no retry needed)", got)
	}
}

// TestAwaitClusterCACertificate_RetriesTransientNotLeader proves a bounded
// number of *service.NotLeaderError answers are retried until the
// provider's read settles onto the leader, without the caller having to
// distinguish this from any other kind of failure.
func TestAwaitClusterCACertificate_RetriesTransientNotLeader(t *testing.T) {
	stub := &stubFlakyClusterCAProvider{notLeaderUntil: 3}
	cert, err := awaitClusterCACertificate(context.Background(), stub, time.Second, time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("awaitClusterCACertificate: %v", err)
	}
	if cert == nil {
		t.Fatal("expected a non-nil certificate")
	}
	if got := stub.callCount(); got != 3 {
		t.Fatalf("Certificate called %d times, want 3 (two transient refusals then success)", got)
	}
}

// TestAwaitClusterCACertificate_OtherErrorsDoNotRetry proves a non-not-leader
// error is returned on the FIRST call, with no backoff sleep and no retry:
// waiting cannot fix a sealed barrier or corrupt material.
func TestAwaitClusterCACertificate_OtherErrorsDoNotRetry(t *testing.T) {
	boom := errors.New("store unreachable")
	stub := &stubFlakyClusterCAProvider{otherErr: boom}
	start := time.Now()
	_, err := awaitClusterCACertificate(context.Background(), stub, time.Minute, 200*time.Millisecond, time.Second)
	if !errors.Is(err, boom) {
		t.Fatalf("awaitClusterCACertificate = %v, want %v", err, boom)
	}
	if elapsed := time.Since(start); elapsed >= 200*time.Millisecond {
		t.Fatalf("awaitClusterCACertificate took %s, want an immediate return with no backoff sleep", elapsed)
	}
	if got := stub.callCount(); got != 1 {
		t.Fatalf("Certificate called %d times, want 1 (no retry on a non-transient error)", got)
	}
}

// TestAwaitClusterCACertificate_DeadlineExceededReturnsErr proves a
// persistent not-leader answer eventually gives up rather than retrying
// forever, once the deadline has passed, and still returns the typed
// *service.NotLeaderError rather than a generic timeout.
func TestAwaitClusterCACertificate_DeadlineExceededReturnsErr(t *testing.T) {
	stub := &stubFlakyClusterCAProvider{notLeaderUntil: 0} // never succeeds
	_, err := awaitClusterCACertificate(context.Background(), stub, 30*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond)
	var notLeader *service.NotLeaderError
	if !errors.As(err, &notLeader) {
		t.Fatalf("awaitClusterCACertificate = %v (%T), want *service.NotLeaderError", err, err)
	}
	if got := stub.callCount(); got < 2 {
		t.Fatalf("Certificate called %d times, want at least 2 (at least one retry before the deadline)", got)
	}
}

// TestAwaitClusterCACertificate_ContextCanceledReturnsErr proves a shutdown
// signal during the retry loop is honored immediately rather than waiting
// out the full backoff deadline.
func TestAwaitClusterCACertificate_ContextCanceledReturnsErr(t *testing.T) {
	stub := &stubFlakyClusterCAProvider{notLeaderUntil: 0} // never succeeds
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := awaitClusterCACertificate(ctx, stub, time.Minute, time.Minute, time.Minute)
	var notLeader *service.NotLeaderError
	if !errors.As(err, &notLeader) {
		t.Fatalf("awaitClusterCACertificate = %v (%T), want *service.NotLeaderError", err, err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("awaitClusterCACertificate took %s with an already-canceled context, want an immediate return", elapsed)
	}
}
