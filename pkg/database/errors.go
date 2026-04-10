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

package database

import "strconv"

// InvalidNameError is returned when a database name is empty or contains
// characters outside the allowed set (alphanumeric, hyphens, underscores).
type InvalidNameError struct {
	Name string
}

// Error returns a human-readable description of the invalid name.
func (e *InvalidNameError) Error() string {
	return "database: invalid database name: " + strconv.Quote(e.Name)
}

// InvalidEngineError is returned when an unrecognized engine type is specified.
type InvalidEngineError struct {
	Engine string
}

// Error returns a human-readable description of the invalid engine type.
func (e *InvalidEngineError) Error() string {
	return "database: invalid engine type: " + strconv.Quote(e.Engine)
}

// InvalidBackendError is returned when an unrecognized backend type is specified.
type InvalidBackendError struct {
	Backend string
}

// Error returns a human-readable description of the invalid backend type.
func (e *InvalidBackendError) Error() string {
	return "database: invalid backend type: " + strconv.Quote(e.Backend)
}

// AlreadyExistsError is returned when attempting to create a database with
// a name that is already in use.
type AlreadyExistsError struct {
	Name string
}

// Error returns a human-readable description of the duplicate database.
func (e *AlreadyExistsError) Error() string {
	return "database: database already exists: " + strconv.Quote(e.Name)
}

// NotFoundError is returned when a requested database does not exist.
type NotFoundError struct {
	Name string
}

// Error returns a human-readable description of the missing database.
func (e *NotFoundError) Error() string {
	return "database: database not found: " + strconv.Quote(e.Name)
}

// NotOpenError is returned when an operation is attempted on a database
// that is not in the open state.
type NotOpenError struct {
	Name string
}

// Error returns a human-readable description of the not-open condition.
func (e *NotOpenError) Error() string {
	return "database: database not open: " + strconv.Quote(e.Name)
}

// OpenError is returned when the underlying storage fails to open.
type OpenError struct {
	Name string
	Err  error
}

// Error returns a human-readable description of the open failure.
func (e *OpenError) Error() string {
	return "database: failed to open database " + strconv.Quote(e.Name) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *OpenError) Unwrap() error {
	return e.Err
}

// CloseError is returned when the database fails to close cleanly.
type CloseError struct {
	Name string
	Err  error
}

// Error returns a human-readable description of the close failure.
func (e *CloseError) Error() string {
	return "database: failed to close database " + strconv.Quote(e.Name) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *CloseError) Unwrap() error {
	return e.Err
}

// MetaReadError is returned when reading the on-disk metadata (meta.json) fails.
type MetaReadError struct {
	Path string
	Err  error
}

// Error returns a human-readable description of the metadata read failure.
func (e *MetaReadError) Error() string {
	return "database: failed to read database metadata " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *MetaReadError) Unwrap() error {
	return e.Err
}

// MetaWriteError is returned when writing the on-disk metadata (meta.json) fails.
type MetaWriteError struct {
	Path string
	Err  error
}

// Error returns a human-readable description of the metadata write failure.
func (e *MetaWriteError) Error() string {
	return "database: failed to write database metadata " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *MetaWriteError) Unwrap() error {
	return e.Err
}

// DropError is returned when a database cannot be dropped (deleted).
type DropError struct {
	Name string
	Err  error
}

// Error returns a human-readable description of the drop failure.
func (e *DropError) Error() string {
	return "database: failed to drop database " + strconv.Quote(e.Name) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *DropError) Unwrap() error {
	return e.Err
}

// StorageCreateError is returned when the storage layer cannot be initialized
// for a database with the specified backend.
type StorageCreateError struct {
	Name    string
	Backend string
	Err     error
}

// Error returns a human-readable description of the storage creation failure.
func (e *StorageCreateError) Error() string {
	return "database: failed to create " + e.Backend + " storage for database " + strconv.Quote(e.Name) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *StorageCreateError) Unwrap() error {
	return e.Err
}

// InvalidModeError is returned when an unrecognized database mode is specified.
type InvalidModeError struct {
	Mode string
}

// Error returns a human-readable description of the invalid mode.
func (e *InvalidModeError) Error() string {
	return "database: invalid database mode: " + strconv.Quote(e.Mode)
}

// ConnectionRequiredError is returned when a remote-mode database is missing
// its connection configuration.
type ConnectionRequiredError struct{}

// Error returns a human-readable description of the missing connection config.
func (e *ConnectionRequiredError) Error() string {
	return "database: connection configuration is required for remote mode"
}

// InvalidRaftClusterModeError is returned when an unrecognized Raft cluster
// mode is specified.
type InvalidRaftClusterModeError struct {
	Mode string
}

// Error returns a human-readable description of the invalid Raft cluster mode.
func (e *InvalidRaftClusterModeError) Error() string {
	return "database: invalid raft cluster mode: " + strconv.Quote(e.Mode)
}

// InvalidDiscoveryMethodError is returned when an unrecognized discovery
// method is specified.
type InvalidDiscoveryMethodError struct {
	Method string
}

// Error returns a human-readable description of the invalid discovery method.
func (e *InvalidDiscoveryMethodError) Error() string {
	return "database: invalid discovery method: " + strconv.Quote(e.Method)
}

// DiscoveryConfigRequiredError is returned when a discovery method is selected
// but its corresponding configuration is missing.
type DiscoveryConfigRequiredError struct {
	Method string
}

// Error returns a human-readable description of the missing discovery config.
func (e *DiscoveryConfigRequiredError) Error() string {
	return "database: discovery configuration required for method: " + strconv.Quote(e.Method)
}

// DiscoverySharedSecretRequiredError is returned when a discovery method
// requires a shared secret but none was provided.
type DiscoverySharedSecretRequiredError struct {
	Method string
}

// Error returns a human-readable description of the missing shared secret.
func (e *DiscoverySharedSecretRequiredError) Error() string {
	return "database: shared secret required for discovery method: " + strconv.Quote(e.Method)
}

// DiscoveryDomainRequiredError is returned when DNS discovery is selected
// but the domain field is empty.
type DiscoveryDomainRequiredError struct{}

// Error returns a human-readable description of the missing domain.
func (e *DiscoveryDomainRequiredError) Error() string {
	return "database: domain is required for dns discovery"
}

// DiscoveryEndpointRequiredError is returned when token discovery is selected
// but the endpoint field is empty.
type DiscoveryEndpointRequiredError struct{}

// Error returns a human-readable description of the missing endpoint.
func (e *DiscoveryEndpointRequiredError) Error() string {
	return "database: endpoint is required for token discovery"
}

// DiscoveryTokenRequiredError is returned when token discovery is selected
// but the token field is empty.
type DiscoveryTokenRequiredError struct{}

// Error returns a human-readable description of the missing token.
func (e *DiscoveryTokenRequiredError) Error() string {
	return "database: token is required for token discovery"
}

// DiscoveryClusterSizeError is returned when the cluster size for token
// discovery is less than 1.
type DiscoveryClusterSizeError struct {
	Size int
}

// Error returns a human-readable description of the invalid cluster size.
func (e *DiscoveryClusterSizeError) Error() string {
	return "database: invalid cluster size for token discovery: " + strconv.Itoa(e.Size)
}

// EtcdListenAddrRequiredError is returned when etcd is enabled but no listen
// address is configured.
type EtcdListenAddrRequiredError struct{}

// Error returns a human-readable description of the missing etcd listen address.
func (e *EtcdListenAddrRequiredError) Error() string {
	return "database: etcd listen_addr is required when etcd is enabled"
}

// MigrationError is returned when a step in the one-time layout migration fails.
type MigrationError struct {
	Step string
	Err  error
}

// Error returns a human-readable description of the migration failure.
func (e *MigrationError) Error() string {
	return "database: migration failed at step " + strconv.Quote(e.Step) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *MigrationError) Unwrap() error {
	return e.Err
}

// TenantAccessDeniedError is returned when a tenant attempts to access
// a database that belongs to a different tenant.
type TenantAccessDeniedError struct {
	TenantID     string
	DatabaseName string
}

// Error returns a human-readable description of the access denial.
func (e *TenantAccessDeniedError) Error() string {
	return "database: tenant " + strconv.Quote(e.TenantID) +
		" access denied for database " + strconv.Quote(e.DatabaseName)
}

// TenantIDRequiredError is returned when a tenant-scoped operation is
// called without a tenant ID in the context.
type TenantIDRequiredError struct {
	Operation string
}

// Error returns a human-readable description.
func (e *TenantIDRequiredError) Error() string {
	return "database: tenant ID required for operation: " + e.Operation
}

// SealedError is returned when operations are attempted on a sealed database.
type SealedError struct{}

// Error returns a human-readable description of the sealed condition.
func (e *SealedError) Error() string {
	return "database: operation rejected \u2014 database is sealed"
}

// BarrierDecryptError is returned when barrier decryption fails for a key.
type BarrierDecryptError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the decryption failure.
func (e *BarrierDecryptError) Error() string {
	return "database: barrier decrypt failed for key " + strconv.Quote(e.Key) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *BarrierDecryptError) Unwrap() error {
	return e.Err
}

// BarrierEncryptError is returned when barrier encryption fails for a key.
type BarrierEncryptError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the encryption failure.
func (e *BarrierEncryptError) Error() string {
	return "database: barrier encrypt failed for key " + strconv.Quote(e.Key) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *BarrierEncryptError) Unwrap() error {
	return e.Err
}

// InvalidStorageCodecError is returned when a storage codec name is specified
// that is not registered in the current build.
type InvalidStorageCodecError struct {
	Codec string
}

// Error returns a human-readable description of the invalid storage codec.
func (e *InvalidStorageCodecError) Error() string {
	return "database: invalid or unsupported storage codec: " + strconv.Quote(e.Codec)
}
