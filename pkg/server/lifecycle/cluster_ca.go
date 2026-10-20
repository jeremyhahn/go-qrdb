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

package lifecycle

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

const (
	// clusterCAEstablishRetryDeadline bounds how long AwaitClusterCACertificate
	// retries a transient read (clusterCAReadSettling) before giving up. Right
	// after this node's own boot, the system shard's leadership can still be
	// settling -- even on a single-node shard -- so the first answer this
	// node sees can be "not leader, leader unknown". Bounded so a genuinely
	// stuck shard still fails rather than hanging the caller forever.
	clusterCAEstablishRetryDeadline = 30 * time.Second

	// clusterCAEstablishRetryBaseDelay is the first backoff interval between
	// retries of a transient not-leader read.
	clusterCAEstablishRetryBaseDelay = 100 * time.Millisecond

	// clusterCAEstablishRetryMaxDelay caps the backoff interval, doubled on
	// each retry starting from clusterCAEstablishRetryBaseDelay.
	clusterCAEstablishRetryMaxDelay = 2 * time.Second
)

// AwaitClusterCACertificate calls provider.Certificate, retrying with
// bounded exponential backoff while the answer is transient -- a
// *service.NotLeaderError or quicraft.ErrReadIndexNotReady (see
// clusterCAReadSettling): the system shard's leadership can still be
// settling immediately after this node's own boot, even on a single-node
// shard, and this node's own read can lose that race. Any other error class
// (a corrupt record, a sealed barrier, no material at all) is returned
// immediately: waiting cannot resolve those, and retrying them would only
// slow down a boot that was always going to fail.
//
// It is a startup read (server.RunStartupRead): bounded by ctx (so a
// shutdown signal during the retry loop is honored immediately), by
// deadline, measured from the first call, and on each attempt by
// server.StartupReadAttemptTimeout, so neither a genuinely stuck shard nor
// one read that never returns hangs the caller. Past the deadline it fails
// with *server.StartupReadError, which unwraps to the last attempt's error.
//
// This is the ESTABLISHING call every composer with a self-issued cluster
// (n.ClusterCAProv != nil) must make once, deliberately, during startup --
// see EstablishClusterCA, which every composer should prefer calling
// directly. It is exported separately because cmd/qrdbd's own
// confirmClusterCAPersisted needs the certificate this returns (to decide
// whether to clean up a local ca-key.pem cache), which EstablishClusterCA's
// simpler signature does not expose.
func AwaitClusterCACertificate(ctx context.Context, provider cakey.Provider, deadline, baseDelay, maxDelay time.Duration) (*x509.Certificate, error) {
	return server.RunStartupRead(ctx, server.StartupRead{
		Op:        "cluster CA certificate",
		Budget:    deadline,
		Delay:     baseDelay,
		MaxDelay:  maxDelay,
		Retryable: clusterCAReadSettling,
	}, provider.Certificate)
}

// clusterCAReadSettling reports whether err is the system shard still
// settling rather than a failure waiting cannot fix: a *service.NotLeaderError
// (leadership in flux), or quicraft.ErrReadIndexNotReady -- a leader elected a
// moment ago refuses a linearizable read until it has committed an entry in
// its own term. A restarted node reaches this read right after the unseal,
// which is exactly that window, and treating it as fatal made the daemon exit.
func clusterCAReadSettling(err error) bool {
	if errors.Is(err, quicraft.ErrReadIndexNotReady) {
		return true
	}
	var notLeader *service.NotLeaderError
	return errors.As(err, &notLeader)
}

// AwaitReplicatedClusterCACertificate is AwaitClusterCACertificate for a
// node that holds no copy of the cluster CA key: it adopts only the cached
// certificate, so its provider loads Key A from the replicated store and can
// never generate one. A founding member other than the CA's issuer reads
// before the issuer has persisted Key A -- the write needs that member for
// quorum -- so here a *cakey.NotInitializedError is the write still in
// flight and is retried within deadline like the settling errors, rather than
// failing the boot.
func AwaitReplicatedClusterCACertificate(ctx context.Context, provider cakey.Provider, deadline, baseDelay, maxDelay time.Duration) (*x509.Certificate, error) {
	return server.RunStartupRead(ctx, server.StartupRead{
		Op:        "replicated cluster CA certificate",
		Budget:    deadline,
		Delay:     baseDelay,
		MaxDelay:  maxDelay,
		Retryable: clusterCAReplicating,
	}, provider.Certificate)
}

// clusterCAReplicating reports whether err is one clusterCAReadSettling
// retries, or Key A not yet in the replicated store.
func clusterCAReplicating(err error) bool {
	var notInitialized *cakey.NotInitializedError
	return clusterCAReadSettling(err) || errors.As(err, &notInitialized)
}

// EstablishClusterCA triggers n.ClusterCAProv's lazy adopt-or-load (a
// Certificate call is enough; see cakey.SoftwareProvider's "material"
// accessor) as a DELIBERATE startup action.
//
// Before go-qrdb#591's review (B5), the dynamic client-auth trust pool's own
// cluster-CA resolution (pkg/clienttrust.Pool, via
// ceremony.ProviderCA.CACertificate) called cakey.Provider.Certificate
// directly, which auto-establishes a brand-new CA the first time nothing
// exists yet -- so in practice the first TLS handshake a listener received
// was ALSO what established Key A for a self-issued cluster, an accidental
// side effect of a connection attempt. ProviderCA.CACertificate now prefers
// cakey.CertOnlyProvider.CertificateOnly, which never does that (a
// handshake must never generate a CA), which means establishment no longer
// happens implicitly and every composer must trigger it explicitly instead.
//
// Every composer (qrdbd, the SDK node builders) must call this once, after
// BuildServices succeeds and before the first client-facing listener can
// accept a connection. A nil n.ClusterCAProv (no cluster-CA concept, or
// BYO-TLS -- lifecycle.Infra.NoClusterCAKey) is a no-op, never an error.
func EstablishClusterCA(ctx context.Context, n *Node) error {
	if n == nil || n.ClusterCAProv == nil {
		return nil
	}
	if _, err := AwaitClusterCACertificate(ctx, n.ClusterCAProv,
		clusterCAEstablishRetryDeadline, clusterCAEstablishRetryBaseDelay, clusterCAEstablishRetryMaxDelay); err != nil {
		return &ClusterCAEstablishError{Err: err}
	}
	return nil
}

// RemoveClusterCAKeyCache deletes the cluster CA private key a
// self-generated identity cached in dataDir (ca-key.pem), once the key is
// durably in the replicated store: the one cleanup cmd/qrdbd and sdk/go/node
// both run after the cluster CA is established, so neither leaves a CA key on
// disk the other would not. A key that is not there is not an error. A key
// that cannot be removed is logged at ERROR and not returned: the key is
// persisted either way, so the stale copy is a cleanup failure, not a
// correctness one, but a CA key lingering on disk is the state this exists to
// eliminate.
func RemoveClusterCAKeyCache(dataDir string, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	keyPath := filepath.Join(dataDir, "ca-key.pem")
	if err := os.Remove(keyPath); err != nil {
		if !os.IsNotExist(err) {
			logger.Error("cluster CA key persisted to the replicated store, but the local cache could not be removed",
				slog.String("path", keyPath), slog.String("error", err.Error()))
		}
		return
	}
	logger.Info("cluster CA key persisted to the replicated store; local file cache removed", slog.String("path", keyPath))
}

// clusterCAAdminSigner returns the firstboot.CertSigner that signs a tenant's
// first-admin CSR with the cluster CA behind p, resolved fresh on every call
// (ceremony.ProviderCA) so a re-seal or HSM failover is reflected at once, and
// names the admin's SPIFFE ID under the cluster's trustDomain.
func clusterCAAdminSigner(p cakey.Provider, trustDomain string) (firstboot.CertSigner, error) {
	clusterCA, err := ceremony.NewProviderCA(p)
	if err != nil {
		return nil, err
	}
	return ceremony.NewAdminCSRSigner(clusterCA, trustDomain)
}

// ClusterCAEstablishError is returned by EstablishClusterCA when the
// cluster CA (Key A) provider's first use -- adopting in-memory bootstrap
// material into the replicated store, or loading what a prior boot already
// persisted -- fails.
type ClusterCAEstablishError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ClusterCAEstablishError) Error() string {
	return "lifecycle: cluster CA establishment failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ClusterCAEstablishError) Unwrap() error {
	return e.Err
}
