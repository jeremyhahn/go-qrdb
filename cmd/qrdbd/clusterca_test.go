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
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

// TestConfirmClusterCAPersisted_UnremovableLocalKeyIsNotFatal: once the CA is
// confirmed durable, a local cache that cannot be removed is a cleanup
// failure, not a boot failure. A non-empty directory in ca-key.pem's place
// makes the remove fail with something other than "not exist".
func TestConfirmClusterCAPersisted_UnremovableLocalKeyIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ca-key.pem")
	if err := os.MkdirAll(filepath.Join(keyPath, "pinned"), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if err := confirmClusterCAPersisted(context.Background(), &stubClusterCAProvider{}, dir, true); err != nil {
		t.Fatalf("confirmClusterCAPersisted = %v, want nil: an unremovable cache must not fail the boot", err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("stat %s: %v; the unremovable entry should remain", keyPath, err)
	}
}

// TestConfirmClusterCAPersisted_ProviderFailureKeepsTheCause: the fatal error
// names the persistence check and still reaches the provider's own error.
func TestConfirmClusterCAPersisted_ProviderFailureKeepsTheCause(t *testing.T) {
	boom := errors.New("store unreachable")
	err := confirmClusterCAPersisted(context.Background(), &stubClusterCAProvider{certErr: boom}, t.TempDir(), false)
	if !errors.Is(err, boom) {
		t.Fatalf("confirmClusterCAPersisted = %v, want it to wrap the provider error", err)
	}
	if want := "qrdbd: cluster CA persistence check failed: store unreachable"; err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
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
