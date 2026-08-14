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

package transport

import "strconv"

// ConfigError is returned when the transport configuration is invalid.
type ConfigError struct {
	Field  string
	Reason string
}

// Error returns a human-readable description.
func (e *ConfigError) Error() string {
	return "transport: invalid config field " + e.Field + ": " + e.Reason
}

// ConnectionError is returned when a transport connection operation fails.
type ConnectionError struct {
	Address string
	Err     error
}

// Error returns a human-readable description.
func (e *ConnectionError) Error() string {
	return "transport: connection failed to " + e.Address + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ConnectionError) Unwrap() error {
	return e.Err
}

// RequestError is returned when a transport request fails.
type RequestError struct {
	Method     string
	StatusCode int
	Err        error
}

// Error returns a human-readable description.
func (e *RequestError) Error() string {
	msg := "transport: request " + e.Method + " failed"
	if e.StatusCode != 0 {
		msg += " (status " + strconv.Itoa(e.StatusCode) + ")"
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *RequestError) Unwrap() error {
	return e.Err
}

// KeyNotFoundError is returned when a KV get operation finds no
// matching key.
type KeyNotFoundError struct {
	Key string
}

// Error returns a human-readable description.
func (e *KeyNotFoundError) Error() string {
	return "transport: key not found: " + e.Key
}

// NotLeaderError is returned when the target node is not the Raft
// leader and cannot process write operations. The leader's identity
// is included when known.
type NotLeaderError struct {
	ShardID       uint64
	LeaderID      uint64
	LeaderAddress string
}

// Error returns a human-readable description.
func (e *NotLeaderError) Error() string {
	if e.LeaderID == 0 {
		return "transport: not leader for shard " + strconv.FormatUint(e.ShardID, 10) + ", leader unknown"
	}
	return "transport: not leader for shard " + strconv.FormatUint(e.ShardID, 10) +
		", leader is replica " + strconv.FormatUint(e.LeaderID, 10) +
		" at " + e.LeaderAddress
}

// TLSError is returned when TLS configuration or handshake fails.
type TLSError struct {
	Err error
}

// Error returns a human-readable description.
func (e *TLSError) Error() string {
	return "transport: TLS error: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TLSError) Unwrap() error {
	return e.Err
}

// ClosedError is returned when an operation is attempted on a closed
// transport.
type ClosedError struct{}

// Error returns a human-readable description.
func (e *ClosedError) Error() string {
	return "transport: connection is closed"
}

// RevisionCompactedError is returned when a requested revision has
// been compacted and is no longer available.
type RevisionCompactedError struct {
	Revision int64
}

// Error returns a human-readable description.
func (e *RevisionCompactedError) Error() string {
	return "transport: revision " + strconv.FormatInt(e.Revision, 10) + " has been compacted"
}

// RevisionNotFoundError is returned when a requested revision does
// not exist.
type RevisionNotFoundError struct {
	Revision int64
}

// Error returns a human-readable description.
func (e *RevisionNotFoundError) Error() string {
	return "transport: revision " + strconv.FormatInt(e.Revision, 10) + " not found"
}

// LeaseNotFoundError is returned when a lease ID does not exist.
type LeaseNotFoundError struct {
	LeaseID int64
}

// Error returns a human-readable description.
func (e *LeaseNotFoundError) Error() string {
	return "transport: lease not found: " + strconv.FormatInt(e.LeaseID, 10)
}

// LeaseExpiredError is returned when a lease has already expired.
type LeaseExpiredError struct {
	LeaseID int64
}

// Error returns a human-readable description.
func (e *LeaseExpiredError) Error() string {
	return "transport: lease expired: " + strconv.FormatInt(e.LeaseID, 10)
}

// WatchCanceledError is returned when a watch stream is canceled.
type WatchCanceledError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *WatchCanceledError) Error() string {
	if e.Reason == "" {
		return "transport: watch canceled"
	}
	return "transport: watch canceled: " + e.Reason
}

// WatchCompactedError indicates a watch requested a start revision that has
// already been compacted on the server, so the intervening history can no
// longer be replayed. The caller's remedy is a fresh read followed by a new
// watch from the current revision — see ClassifyWatchError.
type WatchCompactedError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *WatchCompactedError) Error() string {
	return "transport: watch start revision has been compacted: " + e.Reason
}

// WatchStartRevisionUnsupportedError indicates a watch requested a start
// revision against a server-side store with no change history attached (a
// non-MVCC store), so replay is impossible.
type WatchStartRevisionUnsupportedError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *WatchStartRevisionUnsupportedError) Error() string {
	return "transport: watch start revision not supported: " + e.Reason
}

// IndexExistsError is returned when an index already exists.
type IndexExistsError struct {
	Name string
}

// Error returns a human-readable description.
func (e *IndexExistsError) Error() string {
	return "transport: index already exists: " + e.Name
}

// IndexNotFoundError is returned when an index does not exist.
type IndexNotFoundError struct {
	Name string
}

// Error returns a human-readable description.
func (e *IndexNotFoundError) Error() string {
	return "transport: index not found: " + e.Name
}

// TransactionFailedError is returned when a transaction's preconditions
// did not pass.
type TransactionFailedError struct {
	Revision int64
}

// Error returns a human-readable description.
func (e *TransactionFailedError) Error() string {
	return "transport: transaction preconditions failed at revision " + strconv.FormatInt(e.Revision, 10)
}

// VectorNotFoundError is returned when a vector ID does not exist.
type VectorNotFoundError struct {
	Namespace string
	ID        string
}

// Error returns a human-readable description.
func (e *VectorNotFoundError) Error() string {
	return "transport: vector not found: " + e.Namespace + "/" + e.ID
}

// NamespaceNotFoundError is returned when a vector namespace does not exist.
type NamespaceNotFoundError struct {
	Namespace string
}

// Error returns a human-readable description.
func (e *NamespaceNotFoundError) Error() string {
	return "transport: namespace not found: " + e.Namespace
}

// NamespaceExistsError is returned when a vector namespace already exists.
type NamespaceExistsError struct {
	Namespace string
}

// Error returns a human-readable description.
func (e *NamespaceExistsError) Error() string {
	return "transport: namespace already exists: " + e.Namespace
}

// DimensionMismatchError is returned when a vector's dimensionality
// does not match the namespace configuration.
type DimensionMismatchError struct {
	Expected int
	Got      int
}

// Error returns a human-readable description.
func (e *DimensionMismatchError) Error() string {
	return "transport: dimension mismatch: expected " + strconv.Itoa(e.Expected) + ", got " + strconv.Itoa(e.Got)
}

// NotImplementedError is returned when a transport method is not yet
// implemented.
type NotImplementedError struct {
	Method string
}

// Error returns a human-readable description.
func (e *NotImplementedError) Error() string {
	return "transport: method not implemented: " + e.Method
}

// UserNotFoundError is returned when a user ID does not exist.
type UserNotFoundError struct {
	ID string
}

// Error returns a human-readable description.
func (e *UserNotFoundError) Error() string {
	return "transport: user not found: " + e.ID
}

// UserExistsError is returned when a username already exists within
// the tenant.
type UserExistsError struct {
	Username string
}

// Error returns a human-readable description.
func (e *UserExistsError) Error() string {
	return "transport: user already exists: " + e.Username
}

// RoleNotFoundError is returned when a role ID does not exist.
type RoleNotFoundError struct {
	ID string
}

// Error returns a human-readable description.
func (e *RoleNotFoundError) Error() string {
	return "transport: role not found: " + e.ID
}

// RoleExistsError is returned when a role name already exists within
// the tenant.
type RoleExistsError struct {
	Name string
}

// Error returns a human-readable description.
func (e *RoleExistsError) Error() string {
	return "transport: role already exists: " + e.Name
}

// CertNotFoundError is returned when a certificate serial does not exist.
type CertNotFoundError struct {
	Serial string
}

// Error returns a human-readable description.
func (e *CertNotFoundError) Error() string {
	return "transport: certificate not found: " + e.Serial
}

// DatabaseNotFoundError is returned when a database name does not exist.
type DatabaseNotFoundError struct {
	Name string
}

// Error returns a human-readable description.
func (e *DatabaseNotFoundError) Error() string {
	return "transport: database not found: " + e.Name
}

// DatabaseExistsError is returned when a database name already exists.
type DatabaseExistsError struct {
	Name string
}

// Error returns a human-readable description.
func (e *DatabaseExistsError) Error() string {
	return "transport: database already exists: " + e.Name
}

// ShardNotFoundError is returned when a shard ID does not exist on
// the target host.
type ShardNotFoundError struct {
	ShardID uint64
}

// Error returns a human-readable description.
func (e *ShardNotFoundError) Error() string {
	return "transport: shard not found: " + strconv.FormatUint(e.ShardID, 10)
}

// ShardStartError is returned when starting a Raft shard fails.
type ShardStartError struct {
	ShardID uint64
	Err     error
}

// Error returns a human-readable description.
func (e *ShardStartError) Error() string {
	return "transport: failed to start shard " + strconv.FormatUint(e.ShardID, 10) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ShardStartError) Unwrap() error {
	return e.Err
}

// ShardStopError is returned when stopping a Raft shard fails.
type ShardStopError struct {
	ShardID uint64
	Err     error
}

// Error returns a human-readable description.
func (e *ShardStopError) Error() string {
	return "transport: failed to stop shard " + strconv.FormatUint(e.ShardID, 10) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ShardStopError) Unwrap() error {
	return e.Err
}

// ShardStateError is returned when querying shard Raft state fails.
type ShardStateError struct {
	ShardID uint64
	Err     error
}

// Error returns a human-readable description.
func (e *ShardStateError) Error() string {
	return "transport: failed to get state for shard " + strconv.FormatUint(e.ShardID, 10) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ShardStateError) Unwrap() error {
	return e.Err
}

// TopologyError is returned when querying the cluster topology fails.
type TopologyError struct {
	Err error
}

// Error returns a human-readable description.
func (e *TopologyError) Error() string {
	return "transport: failed to get topology: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TopologyError) Unwrap() error {
	return e.Err
}

// NoStateMachineFactoryError is returned when StartShard is called
// but no state machine factory has been registered.
type NoStateMachineFactoryError struct{}

// Error returns a human-readable description.
func (e *NoStateMachineFactoryError) Error() string {
	return "transport: no state machine factory registered for StartShard"
}

// InvalidWriteModeError is returned when an unrecognized write mode
// string is provided.
type InvalidWriteModeError struct {
	Mode string
}

// Error returns a human-readable description.
func (e *InvalidWriteModeError) Error() string {
	return "transport: invalid write mode: " + e.Mode
}

// InvalidConsistencyLevelError is returned when an unrecognized
// consistency level string is provided.
type InvalidConsistencyLevelError struct {
	Level string
}

// Error returns a human-readable description.
func (e *InvalidConsistencyLevelError) Error() string {
	return "transport: invalid consistency level: " + e.Level
}

// ---------------------------------------------------------------------------
// Backup errors
// ---------------------------------------------------------------------------

// BackupNotFoundError is returned when a backup ID does not exist.
type BackupNotFoundError struct {
	ID string
}

// Error returns a human-readable description.
func (e *BackupNotFoundError) Error() string {
	return "transport: backup not found: " + e.ID
}

// BackupCreateError is returned when backup creation fails.
type BackupCreateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *BackupCreateError) Error() string {
	return "transport: backup creation failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *BackupCreateError) Unwrap() error {
	return e.Err
}

// BackupRestoreError is returned when backup restoration fails.
type BackupRestoreError struct {
	BackupID string
	Err      error
}

// Error returns a human-readable description.
func (e *BackupRestoreError) Error() string {
	return "transport: backup restore failed for " + e.BackupID + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *BackupRestoreError) Unwrap() error {
	return e.Err
}

// BackupVerifyError is returned when backup verification fails.
type BackupVerifyError struct {
	BackupID string
	Err      error
}

// Error returns a human-readable description.
func (e *BackupVerifyError) Error() string {
	return "transport: backup verification failed for " + e.BackupID + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *BackupVerifyError) Unwrap() error {
	return e.Err
}

// BackupEmptyDestinationError is returned when a backup request has
// an empty destination.
type BackupEmptyDestinationError struct{}

// Error returns a human-readable description.
func (e *BackupEmptyDestinationError) Error() string {
	return "transport: backup destination must not be empty"
}

// BackupEmptyIDError is returned when a backup operation requires an
// ID but none was provided.
type BackupEmptyIDError struct{}

// Error returns a human-readable description.
func (e *BackupEmptyIDError) Error() string {
	return "transport: backup ID must not be empty"
}

// BackupChecksumMismatchError is returned when a backup's computed
// checksum does not match the stored checksum.
type BackupChecksumMismatchError struct {
	Expected string
	Actual   string
}

// Error returns a human-readable description.
func (e *BackupChecksumMismatchError) Error() string {
	return "transport: backup checksum mismatch: expected " + e.Expected + ", got " + e.Actual
}

// ---------------------------------------------------------------------------
// Rebalance errors
// ---------------------------------------------------------------------------

// ShardStatsError is returned when querying shard statistics fails.
type ShardStatsError struct {
	ShardID uint64
	Err     error
}

// Error returns a human-readable description.
func (e *ShardStatsError) Error() string {
	return "transport: get shard stats " + strconv.FormatUint(e.ShardID, 10) + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ShardStatsError) Unwrap() error {
	return e.Err
}

// RebalancePlanError is returned when generating a rebalance plan fails.
type RebalancePlanError struct {
	Err error
}

// Error returns a human-readable description.
func (e *RebalancePlanError) Error() string {
	return "transport: create rebalance plan failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *RebalancePlanError) Unwrap() error {
	return e.Err
}

// RebalanceExecuteError is returned when executing a rebalance plan fails.
type RebalanceExecuteError struct {
	Err error
}

// Error returns a human-readable description.
func (e *RebalanceExecuteError) Error() string {
	return "transport: execute rebalance failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *RebalanceExecuteError) Unwrap() error {
	return e.Err
}

// ImbalanceReportError is returned when generating an imbalance report fails.
type ImbalanceReportError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ImbalanceReportError) Error() string {
	return "transport: get imbalance report failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ImbalanceReportError) Unwrap() error {
	return e.Err
}

// NilRebalancePlanError is returned when ExecuteRebalance is called
// with a nil plan.
type NilRebalancePlanError struct{}

// Error returns a human-readable description.
func (e *NilRebalancePlanError) Error() string {
	return "transport: rebalance plan must not be nil"
}

// BatchIndexError is returned when a batch response is missing the
// expected result at the given index. This indicates a server-side
// protocol violation.
type BatchIndexError struct {
	Index int
}

// Error returns a human-readable description.
func (e *BatchIndexError) Error() string {
	return "transport: batch response missing result at index " + strconv.Itoa(e.Index)
}

// NilRequestError is returned (wrapped in a RequestError carrying the
// operation name) when a client method receives a nil request pointer.
type NilRequestError struct{}

// Error returns a human-readable description.
func (e *NilRequestError) Error() string {
	return "transport: nil request"
}

// UnexpectedStatusError is returned when an HTTP response carries a status
// code other than the one expected (typically 200 OK). Body holds the
// truncated response body for diagnostics.
type UnexpectedStatusError struct {
	StatusCode int
	Body       string
}

// Error returns "unexpected status <code>: <body>".
func (e *UnexpectedStatusError) Error() string {
	return "transport: unexpected status " + strconv.Itoa(e.StatusCode) + ": " + e.Body
}
