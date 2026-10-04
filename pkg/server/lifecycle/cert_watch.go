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

import "context"

// certDeletionWatcher is the capability n.CertProv offers when it is a
// *cert.DefaultCertProvider: WatchCertProviderEvictions type-asserts
// against this narrow, structural shape rather than importing the
// concrete type, so an embedder's own CertProvider (server.WithCertProvider)
// is never required to support it.
type certDeletionWatcher interface {
	WatchForDeletions(ctx context.Context) error
}

// WatchCertProviderEvictions starts n.CertProv's Key B cache eviction
// watchers (DefaultCertProvider.WatchForDeletions: a deleted tenant's
// cached per-tenant CA, and the dynamic trust pool's own cache of it,
// drop immediately on every node, rather than aging out by TTL alone --
// go-qrdb#591's review). A no-op when n.CertProv is nil, or is an
// embedder-supplied CertProvider that does not implement this.
//
// Every composer (qrdbd, the SDK node builders) must call this once, with
// its own long-lived background context (NOT whatever context.Context
// BuildServices itself was called with, which may be canceled once setup
// finishes), right after BuildServices succeeds -- the same convention
// EstablishClusterCA follows, and for the same reason: this starts
// goroutines meant to outlive the call that starts them.
func WatchCertProviderEvictions(ctx context.Context, n *Node) error {
	if n == nil || n.CertProv == nil {
		return nil
	}
	watcher, ok := n.CertProv.(certDeletionWatcher)
	if !ok {
		return nil
	}
	return watcher.WatchForDeletions(ctx)
}
