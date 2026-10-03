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

package firstboot

import (
	"context"
	"crypto/x509"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// ResolveAdminCertForFirstBoot resolves the bootstrap admin's certificate
// for first boot to bind, from the durable hand-off ceremony-initialize
// wrote to store (go-qrdb#548). It is the shared "Phase 2.5" every
// composition in this stack runs between ceremony-initialize and first
// boot's own Initialize/BootstrapSystem call: cmd/qrdbd's runFirstBoot,
// sdk/go/node's FirstBootWithCeremony, and any future composer, so the
// resolution logic exists exactly once.
//
// Under auth.MTLSBindingRequired this is a BOUNDED WAIT
// (AwaitPendingAdminCert): the pending record may not have replicated to
// this node yet (it was written on whichever node ran the ceremony), or the
// admin may already be bound (nothing to do, both report (nil, nil)).
// Under any other policy it is a single, opportunistic, non-blocking read:
// a pending record is never REQUIRED outside "required", so a deployment
// that opted out never waits on it, and its absence is not an error.
//
// A nil store resolves to (nil, nil) unconditionally -- a composer with no
// cluster-wide store (a single-process evaluation that already set
// Dependencies.AdminCertPEM/.CA/.Binder itself) has nothing to resolve here.
func ResolveAdminCertForFirstBoot(ctx context.Context, store PendingAdminCertStore, users AdminLookup, policy auth.MTLSBindingPolicy, timeout time.Duration) ([]byte, error) {
	if store == nil {
		return nil, nil
	}
	if policy == auth.MTLSBindingRequired {
		return AwaitPendingAdminCert(ctx, store, users, timeout)
	}
	certPEM, err := store.Get(ctx, PendingAdminCertKey)
	if err != nil {
		return nil, nil
	}
	return certPEM, nil
}

// AdminCertPoolFromCABundle builds the trust anchor pool
// Dependencies.AdminCertPool chains the resolved admin certificate against,
// from a PEM-encoded CA bundle (ordinarily ceremony.CeremonyCA.CABundle()).
// Returns nil for an empty bundle or one with no parseable certificate --
// the caller falls back to whatever default pool resolution it already has,
// exactly as an empty AdminCertPool always has.
func AdminCertPoolFromCABundle(caBundlePEM []byte) *x509.CertPool {
	if len(caBundlePEM) == 0 {
		return nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caBundlePEM) {
		return nil
	}
	return pool
}
