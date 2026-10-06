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
	"crypto/x509"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

const (
	// clusterCAPersistRetryDeadline bounds how long confirmClusterCAPersisted
	// retries a transient not-leader or read-index-not-ready read before
	// giving up. Right after waitForLeaderElection returns, the system
	// shard's leader has been observed, but this node's own read of it can
	// still race a brief settling window -- even on a single-node shard --
	// so the first answer this node sees can be "not leader, leader
	// unknown", or a new leader's ReadIndex warm-up. Bounded
	// so a genuinely stuck shard still fails the boot instead of hanging
	// it forever.
	clusterCAPersistRetryDeadline = 30 * time.Second

	// clusterCAPersistRetryBaseDelay is the first backoff interval between
	// retries of a transient not-leader read.
	clusterCAPersistRetryBaseDelay = 100 * time.Millisecond

	// clusterCAPersistRetryMaxDelay caps the backoff interval, doubled on
	// each retry starting from clusterCAPersistRetryBaseDelay.
	clusterCAPersistRetryMaxDelay = 2 * time.Second
)

// clusterCABootstrapCertPEM returns the in-memory CA certificate to offer
// the built-in cluster CA provider as bootstrap material, or nil for a
// BYO-TLS bundle (bundle.ExplicitTLS): there, bundle.CACertPEM is the
// operator's own trust anchor, never material this process generated, and
// must never be adopted as Key A. See lifecycle.Infra.NoClusterCAKey, which
// this node also sets so the built-in provider is never even constructed.
func clusterCABootstrapCertPEM(bundle *TLSBundle) []byte {
	if bundle.ExplicitTLS {
		return nil
	}
	return bundle.CACertPEM
}

// clusterCABootstrapKeyPEM is clusterCABootstrapCertPEM's counterpart for
// the CA private key. bundle.CAKeyPEM is already empty for an ExplicitTLS
// bundle -- loadTLSFromFiles never populates it -- but withholding it
// explicitly here keeps the two helpers symmetric and independent of that
// detail.
func clusterCABootstrapKeyPEM(bundle *TLSBundle) []byte {
	if bundle.ExplicitTLS {
		return nil
	}
	return bundle.CAKeyPEM
}

// awaitClusterCACertificate calls provider.Certificate, retrying with
// bounded exponential backoff while the answer is a transient
// *service.NotLeaderError -- the system shard's leadership can still be
// settling immediately after waitForLeaderElection returns, even on a
// single-node shard, and this node's own read can lose that race. Any
// other error class (a corrupt record, a sealed barrier, no material at
// all) is returned immediately: waiting cannot resolve those, and retrying
// them would only slow down a boot that was always going to fail.
//
// Bounded by both ctx (so a shutdown signal during the retry loop is
// honored immediately) and deadline, measured from the first call: a
// genuinely stuck shard fails the boot rather than hanging it.
//
// A thin wrapper over lifecycle.AwaitClusterCACertificate: the retry logic
// itself is shared with every other composer (go-qrdb#591's review, B5's
// EstablishClusterCA), and this qrdbd-local name and signature are kept so
// existing tests in this package stay unchanged.
func awaitClusterCACertificate(ctx context.Context, provider cakey.Provider, deadline, baseDelay, maxDelay time.Duration) (*x509.Certificate, error) {
	return lifecycle.AwaitClusterCACertificate(ctx, provider, deadline, baseDelay, maxDelay)
}

// confirmClusterCAPersisted triggers the cluster CA (Key A) provider's
// lazy adopt-or-load (a Certificate call is enough; see
// cakey.SoftwareProvider's "material" accessor) and, once it succeeds,
// deletes the local ca-key.pem cache -- the file is a bootstrap-only
// artifact once the replicated store durably holds the same key.
//
// hadLocalKey records whether ca-key.pem existed on disk THIS boot (bundle
// loaded or just generated it); when false there is nothing to delete; a
// node past its first successful boot already has none.
//
// A failure here is fatal: proceeding with the data plane up but no
// confirmed, durable cluster CA would let this node issue tenant logins
// while its own cluster identity was never made durable, and silently
// leaves ca-key.pem (if present) as the only copy of a key this process
// might not hold in memory on its next restart.
func confirmClusterCAPersisted(ctx context.Context, provider cakey.Provider, dataDir string, hadLocalKey bool) error {
	if _, err := awaitClusterCACertificate(ctx, provider,
		clusterCAPersistRetryDeadline, clusterCAPersistRetryBaseDelay, clusterCAPersistRetryMaxDelay); err != nil {
		return &ClusterCAPersistError{Err: err}
	}
	if !hadLocalKey {
		return nil
	}

	keyPath := filepath.Join(dataDir, "ca-key.pem")
	if err := os.Remove(keyPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		// Not fatal: the key is durably persisted either way, and the
		// stale local copy is a cleanup failure, not a correctness one.
		// Logged at ERROR because a key file lingering on disk is exactly
		// the state this change set out to eliminate.
		slog.Error("cluster CA key persisted to the replicated store, but the local cache could not be removed",
			slog.String("path", keyPath), slog.String("error", err.Error()))
		return nil
	}
	slog.Info("cluster CA key persisted to the replicated store; local file cache removed", slog.String("path", keyPath))
	return nil
}

// ClusterCAPersistError is returned when the cluster CA (Key A) provider's
// first use -- adopting in-memory bootstrap material into the replicated
// store, or loading what a prior boot already persisted -- fails.
type ClusterCAPersistError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ClusterCAPersistError) Error() string {
	return "qrdbd: cluster CA persistence check failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ClusterCAPersistError) Unwrap() error {
	return e.Err
}
