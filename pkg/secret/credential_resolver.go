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

package secret

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

const (
	credentialScheme = "credential"
	// #nosec G101 -- URI scheme prefix for indirect references, not a credential.
	credentialPrefix = "credential://"
)

// CredentialRetrieveFunc is a function that retrieves a named credential's
// plaintext value. It is the primitive backing type for CredentialResolver.
type CredentialRetrieveFunc func(ctx context.Context, name string) ([]byte, error)

// CredentialRetriever is the minimal interface required to retrieve a named
// credential. It is satisfied by service.CredentialServicer from pkg/service
// without creating an import cycle (pkg/service would eventually import
// pkg/secret for the ResolverRegistry and ConfigProcessor).
type CredentialRetriever interface {
	CredentialRetrieve(ctx context.Context, name string) ([]byte, error)
}

// CredentialResolver resolves "credential://<name>" URIs to named credential
// values stored in the qrdb credential store.
type CredentialResolver struct {
	retrieve CredentialRetrieveFunc
}

// Compile-time interface check.
var _ SecretResolver = (*CredentialResolver)(nil)

// NewCredentialResolver constructs a CredentialResolver backed by retrieve.
func NewCredentialResolver(retrieve CredentialRetrieveFunc) *CredentialResolver {
	return &CredentialResolver{retrieve: retrieve}
}

// Scheme returns the URI scheme this resolver handles.
func (r *CredentialResolver) Scheme() string { return credentialScheme }

// CanResolve reports whether raw is a "credential://" URI.
func (r *CredentialResolver) CanResolve(raw string) bool {
	return strings.HasPrefix(raw, credentialPrefix)
}

// Resolve strips the "credential://" prefix, looks up the named credential
// via the configured retrieve function, and returns the result as a
// SecretValue with StrategyCredential.
//
// Returns ErrNilContext for a nil ctx.
// Returns ResolutionFailedError when the retrieve function is nil.
// Returns InvalidSecretURIError when the name after stripping the prefix is empty.
// Returns SecretNotFoundError when the named credential does not exist.
// Returns ResolutionFailedError{Scheme:"credential"} when retrieve otherwise fails.
func (r *CredentialResolver) Resolve(ctx context.Context, raw string) (*SecretValue, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	if r.retrieve == nil {
		return nil, &ResolutionFailedError{
			Scheme: credentialScheme,
			Raw:    raw,
			Err:    errNilRetrieve,
		}
	}
	name := strings.TrimPrefix(raw, credentialPrefix)
	if name == "" {
		return nil, &InvalidSecretURIError{Raw: raw}
	}
	b, err := r.retrieve(ctx, name)
	if err != nil {
		// A missing credential is a distinct, expected condition; surface it as a
		// typed SecretNotFoundError so callers can tell "no such credential" apart
		// from a backend failure.
		if errors.Is(err, credentials.ErrCredentialNotFound) {
			return nil, &SecretNotFoundError{Scheme: credentialScheme, Name: name}
		}
		return nil, &ResolutionFailedError{Scheme: credentialScheme, Raw: raw, Err: err}
	}
	// NewSecretValue copies b into the enclave; wipe the heap copy the retrieve
	// function handed us so the plaintext does not linger in GC memory,
	// regardless of whether sealing succeeded.
	sv, sealErr := NewSecretValue(raw, b, StrategyCredential)
	secure.Wipe(b)
	if sealErr != nil {
		return nil, &ResolutionFailedError{Scheme: credentialScheme, Raw: raw, Err: sealErr}
	}
	return sv, nil
}

// CredentialResolverFromServicer constructs a CredentialResolver backed by
// svc.CredentialRetrieve. Any concrete type that satisfies CredentialRetriever
// — including service.CredentialServicer from pkg/service — may be passed.
func CredentialResolverFromServicer(svc CredentialRetriever) *CredentialResolver {
	return NewCredentialResolver(svc.CredentialRetrieve)
}

// NewTenantScopedCredentialRetrieve returns a CredentialRetrieveFunc that
// routes credential lookups to the per-tenant CredentialService when the
// request context carries a non-empty tenant ID, falling back to systemSvc
// for system-scope requests.
//
// Per-tenant services are constructed fresh on every call: a tenant barrier
// can be atomically replaced during an enable-encryption rekey
// (StorageBarrier.ReopenKeyed), so caching a per-tenant CredentialService
// would risk serving reads from a stale barrier. A GetTenant sync.Map lookup
// plus a trivial struct initialisation is negligible compared to the
// credential retrieval I/O.
func NewTenantScopedCredentialRetrieve(
	registry *seal.BarrierRegistry,
	systemSvc CredentialRetriever,
	tenantIDFromCtx func(context.Context) (string, bool),
	logger *slog.Logger,
) CredentialRetrieveFunc {
	return func(ctx context.Context, name string) ([]byte, error) {
		if tenantIDFromCtx != nil && registry != nil {
			if tid, ok := tenantIDFromCtx(ctx); ok && tid != "" {
				tb, err := registry.GetTenant(tid)
				if err != nil {
					return nil, err
				}
				svc, err := credentials.NewCredentialService(
					&credentials.Config{Strategy: credentials.StrategyBarrier},
					tb.Barrier(),
					logger,
				)
				if err != nil {
					return nil, err
				}
				return svc.RetrieveCredential(ctx, name)
			}
		}
		if systemSvc == nil {
			return nil, ErrNilSystemCredentialService
		}
		return systemSvc.CredentialRetrieve(ctx, name)
	}
}
