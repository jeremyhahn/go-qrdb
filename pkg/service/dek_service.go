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

package service

import (
	"context"
	"io"

	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// DEKServicer is the business-logic interface for the per-tenant DEK
// keyring, shared by every transport.
//
// Every method takes the tenant the request names, which may be empty. The
// tenant actually acted on is ALWAYS the caller's own — the tenant bound to
// ctx by authentication, or the system tenant for an unbound in-process
// caller. A request naming any other tenant is refused with
// *dek.CrossTenantError, including from the system tenant: the
// system-tenant rule that lifts self-scope permissions across tenants does
// not reach another tenant's keys.
type DEKServicer interface {
	// Status reports the caller's keyring versions. It never returns keys.
	Status(ctx context.Context, tenantID string) (*dek.Status, error)

	// Rotate adds a key version and makes it current.
	Rotate(ctx context.Context, tenantID string) (*dek.Status, error)

	// Retire destroys an old key version. The current version is refused.
	Retire(ctx context.Context, tenantID string, version uint64) (*dek.Status, error)

	// Encrypt seals plaintext under the current version, binding aad.
	Encrypt(ctx context.Context, tenantID string, plaintext, aad []byte) (*dek.Sealed, error)

	// Decrypt opens a ciphertext sealed for the same tenant and aad.
	Decrypt(ctx context.Context, tenantID string, ciphertext, aad []byte) (*dek.Opened, error)

	// Rewrap re-seals a ciphertext under the current version without
	// returning its plaintext.
	Rewrap(ctx context.Context, tenantID string, ciphertext, aad []byte) (*dek.Rewrapped, error)
}

// DEKService implements DEKServicer over a dek.Provider: the software
// dek.Keyring by default, or a composer's device-backed engine.
type DEKService struct {
	provider dek.Provider
}

// Compile-time interface check.
var _ DEKServicer = (*DEKService)(nil)

// NewDEKService returns a DEKService over provider.
func NewDEKService(provider dek.Provider) (*DEKService, error) {
	if provider == nil {
		return nil, &dek.InvalidInputError{Op: "NewDEKService", Field: "provider"}
	}
	return &DEKService{provider: provider}, nil
}

// Close releases the underlying provider's resources when it holds any. For
// the software dek.Keyring that is the cached "software" key-wrap provider, if
// it ever resolved one; mirrors BackupService.Close for the identical reason:
// constructing the provider fresh on every call would leak one open directory
// file descriptor per call. A provider with nothing to release needs no Close.
func (s *DEKService) Close() error {
	if c, ok := s.provider.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// Status implements DEKServicer.
func (s *DEKService) Status(ctx context.Context, tenantID string) (*dek.Status, error) {
	t, err := dekTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return s.provider.Status(ctx, t)
}

// Rotate implements DEKServicer.
func (s *DEKService) Rotate(ctx context.Context, tenantID string) (*dek.Status, error) {
	t, err := dekTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return s.provider.Rotate(ctx, t)
}

// Retire implements DEKServicer.
func (s *DEKService) Retire(ctx context.Context, tenantID string, version uint64) (*dek.Status, error) {
	t, err := dekTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return s.provider.Retire(ctx, t, version)
}

// Encrypt implements DEKServicer.
func (s *DEKService) Encrypt(ctx context.Context, tenantID string, plaintext, aad []byte) (*dek.Sealed, error) {
	t, err := dekTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return s.provider.Encrypt(ctx, t, plaintext, aad)
}

// Decrypt implements DEKServicer.
func (s *DEKService) Decrypt(ctx context.Context, tenantID string, ciphertext, aad []byte) (*dek.Opened, error) {
	t, err := dekTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return s.provider.Decrypt(ctx, t, ciphertext, aad)
}

// Rewrap implements DEKServicer.
func (s *DEKService) Rewrap(ctx context.Context, tenantID string, ciphertext, aad []byte) (*dek.Rewrapped, error) {
	t, err := dekTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return s.provider.Rewrap(ctx, t, ciphertext, aad)
}

// dekTenant resolves the tenant a keyring request acts on: the caller's own,
// whatever the request names. Unlike tenant.EffectiveTenant there is no
// system-tenant exception.
func dekTenant(ctx context.Context, requested string) (string, error) {
	caller := tenant.FromContext(ctx)
	if requested == "" || tenantid.Normalize(requested) == caller {
		return caller, nil
	}
	return "", &dek.CrossTenantError{CallerTenant: caller, RequestedTenant: requested}
}
