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

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// CredentialServicer provides credential management operations including
// storage, retrieval, submission, deletion, enumeration, and versioned
// rotation of named credentials.
type CredentialServicer interface {
	// CredentialStore persists a credential under the given name.
	CredentialStore(ctx context.Context, name string, value []byte) error

	// CredentialRetrieveBuffer returns the credential value for the given
	// name in protected memory. For the manual strategy, this blocks until
	// the credential is submitted or the context is cancelled. The caller
	// owns the returned buffer and MUST Destroy it on every path; a transport
	// copies the value out only into its secret-bearing wire DTO, the single
	// boundary where bytes leave protected memory.
	CredentialRetrieveBuffer(ctx context.Context, name string) (*secure.Buffer, error)

	// CredentialSubmit submits a credential for the manual strategy,
	// notifying any blocked retrievers. For the barrier strategy, this
	// delegates to CredentialStore.
	CredentialSubmit(ctx context.Context, name string, value []byte) error

	// CredentialDelete removes the credential with the given name and
	// all of its non-current historical versions.
	CredentialDelete(ctx context.Context, name string) error

	// CredentialStrategy returns the active credential strategy name.
	CredentialStrategy() string

	// CredentialList returns the names of every stored credential.
	// Values are never returned by enumeration. Returns
	// credentials.ErrEnumerationNotSupported when the active strategy
	// cannot enumerate (reserved for future HSM-only backends).
	CredentialList(ctx context.Context) ([]string, error)

	// CredentialRotate atomically replaces the current value of name
	// with opts.NewValue, returning the previous + new version numbers
	// and the grace expiry for the previous version. Pass GracePeriod
	// == 0 to wipe the previous value immediately.
	CredentialRotate(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error)

	// CredentialRetrieveVersionBuffer returns the stored credential value
	// for a specific version in protected memory. Use version == 0 to mean
	// "current version". The caller owns the returned buffer and MUST
	// Destroy it on every path.
	CredentialRetrieveVersionBuffer(ctx context.Context, name string, version uint64) (*secure.Buffer, error)

	// CredentialListVersions returns the version history (current + all
	// non-expired grace versions) for name. Each entry's Value field is
	// nil; use CredentialRetrieveVersionBuffer to read a specific value.
	CredentialListVersions(ctx context.Context, name string) ([]credentials.CredentialVersion, error)

	// CredentialGCExpired reaps any historical versions of name whose
	// grace period has elapsed. Returns the count reaped.
	CredentialGCExpired(ctx context.Context, name string) (int, error)
}

// CredentialService implements CredentialServicer by delegating to a
// credentials.CredentialService instance.
type CredentialService struct {
	creds *credentials.CredentialService
}

// Compile-time interface check.
var _ CredentialServicer = (*CredentialService)(nil)

// NewCredentialService creates a new CredentialServicer backed by the
// given credentials.CredentialService.
func NewCredentialService(c *credentials.CredentialService) CredentialServicer {
	return &CredentialService{creds: c}
}

// CredentialWireValue copies a retrieved credential out of protected memory
// into the plain []byte that a transport's secret-bearing response DTO
// carries, and destroys buf before it returns, on success and on failure.
//
// This is the single boundary where a credential leaves protected memory: a
// value written into a response is plaintext in encoding/json, net/http,
// protobuf and the transport's own buffers, none of which this repository can
// wipe, so the value is protected from the point this repository first owns it
// to this call and no further. A transport that encodes the DTO itself (REST,
// MCP) wipes the returned slice as soon as it has been written; one that hands
// the DTO to a framework which marshals it after the handler returns (gRPC,
// QUIC) cannot, and says so in its documentation
// (docs/credentials/architecture.md, "Protected-memory boundary").
func CredentialWireValue(buf *secure.Buffer) ([]byte, error) {
	defer buf.Destroy()
	return secure.CopyOut(buf)
}

// CredentialStore persists a credential under the given name by delegating
// to the underlying CredentialService's StoreCredential method.
func (s *CredentialService) CredentialStore(ctx context.Context, name string, value []byte) error {
	return s.creds.StoreCredential(ctx, name, value)
}

// CredentialRetrieveBuffer returns the credential value for the given name in
// protected memory by delegating to the underlying CredentialService's
// RetrieveCredentialBuffer method.
func (s *CredentialService) CredentialRetrieveBuffer(ctx context.Context, name string) (*secure.Buffer, error) {
	return s.creds.RetrieveCredentialBuffer(ctx, name)
}

// CredentialSubmit submits a credential by delegating to the underlying
// CredentialService's SubmitCredential method.
func (s *CredentialService) CredentialSubmit(ctx context.Context, name string, value []byte) error {
	return s.creds.SubmitCredential(ctx, name, value)
}

// CredentialDelete removes a credential by delegating to the underlying
// CredentialService's DeleteCredential method.
func (s *CredentialService) CredentialDelete(ctx context.Context, name string) error {
	return s.creds.DeleteCredential(ctx, name)
}

// CredentialStrategy returns the active credential strategy name by
// delegating to the underlying CredentialService's Strategy method.
func (s *CredentialService) CredentialStrategy() string {
	return s.creds.Strategy()
}

// CredentialList returns the names of every stored credential.
func (s *CredentialService) CredentialList(ctx context.Context) ([]string, error) {
	return s.creds.ListCredentials(ctx)
}

// CredentialRotate atomically replaces the current value of name with
// opts.NewValue, returning version metadata + grace expiry.
func (s *CredentialService) CredentialRotate(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
	return s.creds.RotateCredential(ctx, name, opts)
}

// CredentialRetrieveVersionBuffer returns the stored credential value for a
// specific version (or the current version when version == 0) in protected
// memory.
func (s *CredentialService) CredentialRetrieveVersionBuffer(ctx context.Context, name string, version uint64) (*secure.Buffer, error) {
	return s.creds.RetrieveCredentialVersionBuffer(ctx, name, version)
}

// CredentialListVersions returns the version history (current + grace)
// for name. Each entry's Value field is nil.
func (s *CredentialService) CredentialListVersions(ctx context.Context, name string) ([]credentials.CredentialVersion, error) {
	return s.creds.ListCredentialVersions(ctx, name)
}

// CredentialGCExpired reaps historical versions whose grace period has
// elapsed. Returns the count reaped.
func (s *CredentialService) CredentialGCExpired(ctx context.Context, name string) (int, error) {
	return s.creds.GCExpiredVersions(ctx, name)
}
