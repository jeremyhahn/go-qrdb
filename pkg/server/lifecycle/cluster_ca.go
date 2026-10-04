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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

const (
	// clusterCAEstablishRetryDeadline bounds how long AwaitClusterCACertificate
	// retries a transient *service.NotLeaderError before giving up. Right
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
// bounded exponential backoff while the answer is a transient
// *service.NotLeaderError -- the system shard's leadership can still be
// settling immediately after this node's own boot, even on a single-node
// shard, and this node's own read can lose that race. Any other error class
// (a corrupt record, a sealed barrier, no material at all) is returned
// immediately: waiting cannot resolve those, and retrying them would only
// slow down a boot that was always going to fail.
//
// Bounded by both ctx (so a shutdown signal during the retry loop is
// honored immediately) and deadline, measured from the first call: a
// genuinely stuck shard fails rather than hanging the caller.
//
// This is the ESTABLISHING call every composer with a self-issued cluster
// (n.ClusterCAProv != nil) must make once, deliberately, during startup --
// see EstablishClusterCA, which every composer should prefer calling
// directly. It is exported separately because cmd/qrdbd's own
// confirmClusterCAPersisted needs the certificate this returns (to decide
// whether to clean up a local ca-key.pem cache), which EstablishClusterCA's
// simpler signature does not expose.
func AwaitClusterCACertificate(ctx context.Context, provider cakey.Provider, deadline, baseDelay, maxDelay time.Duration) (*x509.Certificate, error) {
	giveUpAt := time.Now().Add(deadline)
	delay := baseDelay
	for {
		cert, err := provider.Certificate(ctx)
		if err == nil {
			return cert, nil
		}
		var notLeader *service.NotLeaderError
		if !errors.As(err, &notLeader) {
			return nil, err
		}
		if !time.Now().Before(giveUpAt) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(delay):
		}
		if delay *= 2; delay > maxDelay {
			delay = maxDelay
		}
	}
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
