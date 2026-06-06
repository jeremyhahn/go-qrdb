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

// NilHostError is returned when Infra.Host is nil.
type NilHostError struct{}

// Error implements the error interface.
func (e *NilHostError) Error() string {
	return "lifecycle: infra.Host must not be nil"
}

// NilWatchHubError is returned when Infra.WatchHub is nil.
type NilWatchHubError struct{}

// Error implements the error interface.
func (e *NilWatchHubError) Error() string {
	return "lifecycle: infra.WatchHub must not be nil"
}

// NilSealBarrierError is returned when Infra.SealBarrier is required but
// nil. Used by BuildSealServicesOnly which cannot construct a barrier
// service without one.
type NilSealBarrierError struct{}

// Error implements the error interface.
func (e *NilSealBarrierError) Error() string {
	return "lifecycle: infra.SealBarrier must not be nil"
}

// NilCredentialCoreError is returned when Infra.CredentialCoreSvc is
// required but nil. Used by BuildSealServicesOnly because the ceremony
// service cannot be constructed without a credential service.
type NilCredentialCoreError struct{}

// Error implements the error interface.
func (e *NilCredentialCoreError) Error() string {
	return "lifecycle: infra.CredentialCoreSvc must not be nil"
}

// LockServiceError is returned when the distributed lock service fails
// to initialize.
type LockServiceError struct {
	Err error
}

// Error returns a human-readable description.
func (e *LockServiceError) Error() string {
	return "lifecycle: lock service initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *LockServiceError) Unwrap() error {
	return e.Err
}

// VectorStorageError is returned when the vector storage backend fails
// to initialize.
type VectorStorageError struct {
	Err error
}

// Error returns a human-readable description.
func (e *VectorStorageError) Error() string {
	return "lifecycle: vector storage initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *VectorStorageError) Unwrap() error {
	return e.Err
}

// VectorServiceError is returned when the vector service fails to
// initialize.
type VectorServiceError struct {
	Err error
}

// Error returns a human-readable description.
func (e *VectorServiceError) Error() string {
	return "lifecycle: vector service initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *VectorServiceError) Unwrap() error {
	return e.Err
}

// BarrierRegistryError is returned when the barrier registry fails to
// initialize.
type BarrierRegistryError struct {
	Err error
}

// Error returns a human-readable description.
func (e *BarrierRegistryError) Error() string {
	return "lifecycle: barrier registry initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *BarrierRegistryError) Unwrap() error {
	return e.Err
}

// CeremonyServiceError is returned when the ceremony service fails to
// initialize.
type CeremonyServiceError struct {
	Err error
}

// Error returns a human-readable description.
func (e *CeremonyServiceError) Error() string {
	return "lifecycle: ceremony service initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CeremonyServiceError) Unwrap() error {
	return e.Err
}

// CredentialServiceError is returned when the credential service fails
// to initialize.
type CredentialServiceError struct {
	Err error
}

// Error returns a human-readable description.
func (e *CredentialServiceError) Error() string {
	return "lifecycle: credential service initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CredentialServiceError) Unwrap() error {
	return e.Err
}

// TenantServiceError is returned when the tenant lifecycle service fails
// to initialize.
type TenantServiceError struct {
	Err error
}

// Error returns a human-readable description.
func (e *TenantServiceError) Error() string {
	return "lifecycle: tenant service initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TenantServiceError) Unwrap() error {
	return e.Err
}

// UserServiceError is returned when the user store fails to initialize.
type UserServiceError struct {
	Err error
}

// Error returns a human-readable description.
func (e *UserServiceError) Error() string {
	return "lifecycle: user service initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *UserServiceError) Unwrap() error {
	return e.Err
}

// RoleServiceError is returned when the role store fails to initialize.
type RoleServiceError struct {
	Err error
}

// Error returns a human-readable description.
func (e *RoleServiceError) Error() string {
	return "lifecycle: role service initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *RoleServiceError) Unwrap() error {
	return e.Err
}

// EmbeddingCacheError is returned when the embedding LRU cache fails to
// initialize.
type EmbeddingCacheError struct {
	Err error
}

// Error returns a human-readable description.
func (e *EmbeddingCacheError) Error() string {
	return "lifecycle: embedding cache initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *EmbeddingCacheError) Unwrap() error {
	return e.Err
}

// EmbeddingProviderError is returned when an embedding provider fails to
// initialize or register.
type EmbeddingProviderError struct {
	// Name is the provider name from the configuration.
	Name string
	Err  error
}

// Error returns a human-readable description.
func (e *EmbeddingProviderError) Error() string {
	return "lifecycle: embedding provider " + e.Name + " initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *EmbeddingProviderError) Unwrap() error {
	return e.Err
}
