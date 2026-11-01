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

import (
	"errors"
	"strconv"
	"time"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

// ConfigError is returned when the transport configuration is invalid.
type ConfigError struct {
	Field  string
	Reason string
}

// Error returns a human-readable description.
func (e *ConfigError) Error() string {
	return "transport: invalid config field " + e.Field + ": " + e.Reason
}

// DatabaseScopeUnsupportedError is returned when a caller asks for a
// database-scoped client over a transport that cannot confine its data plane
// to that database. It is a refusal, never a fallback: serving the default
// database to a caller that named another would read and write the wrong
// data.
type DatabaseScopeUnsupportedError struct {
	Database string
	// Reason names what the transport is missing.
	Reason string
}

// Error returns a human-readable description.
func (e *DatabaseScopeUnsupportedError) Error() string {
	return "transport: cannot scope to database " + e.Database + ": " + e.Reason
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

	// RetryAfter is how long the server said the caller must wait before a
	// retry can succeed -- a throttled request, an SO PIN lockout, a
	// rate-limited caller -- and zero when it named no delay. Every transport
	// fills it from what the server sent: REST's Retry-After header (whole
	// seconds), gRPC's and the Unix socket's google.rpc.RetryInfo detail,
	// QUIC's error-frame RetryInfo, MCP's retry_after_seconds, and, in
	// process, the refusal's own delay.
	RetryAfter time.Duration

	// Reason and Metadata are the server's structured refusal, when it sent
	// one (pkg/replicaforward.Refusal, domain RefusalDomain): a stable code
	// such as "LEADER_TRANSFER_TIMEOUT" and the fields that rebuild the typed
	// error, so a caller never parses the message. Every wire transport fills
	// them from what the server sent: REST's code/metadata body fields, gRPC's
	// and the Unix socket's google.rpc.ErrorInfo detail, QUIC's error-frame
	// info and MCP's envelope reason/metadata. Both are empty otherwise.
	Reason   string
	Metadata map[string]string
}

// RefusalDomain is the google.rpc.ErrorInfo domain of qrdb's structured
// refusals (pkg/replicaforward.ErrorDomain). A detail in any other domain is
// not one, and does not fill RequestError.Reason.
const RefusalDomain = "qrdb.v1"

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

// CapabilityUnsupportedError reports that the node serving this request was
// not built to perform the operation — an opcode its state machine has no
// handler for, or a subsystem it was constructed without.
//
// It exists so a caller can tell "this node cannot do that" from "this node
// broke". Before go-qrdb#141 a capability refusal reached the client as a
// generic RequestError over the embedded transport and as codes.Internal over
// gRPC, so a healthy node declining a verb it does not implement was
// indistinguishable from a server fault — and a client library that retries on
// internal errors would retry something that can never succeed, the same shape
// as go-qrdb#52.
//
// A consumer that wants to degrade gracefully — fall back from CompareAndSwap
// to a transaction, or from a transaction to a native PutIfAbsent — detects it
// with errors.As rather than by matching message text.
//
// Note it is distinct from the transport-level capability question answered by
// a GuardedKVClient type assertion. That assertion reports whether the
// TRANSPORT carries the verb; this error reports that the STATE MACHINE behind
// it does not implement it. Both can be true at once, which is exactly the case
// on a raft node running a non-default sm_type.
type CapabilityUnsupportedError struct {
	// Message is the server's account of what is missing.
	Message string
}

// Error returns a human-readable description.
func (e *CapabilityUnsupportedError) Error() string {
	return "transport: unsupported capability: " + e.Message
}

// BarrierSealedError reports that the node is up and its barrier is sealed.
//
// A sealed node serves the unseal call and nothing else; every other call is
// refused with this error on every transport. It is how a caller, a health
// probe or a readiness wait tells "reachable and sealed" from "unreachable":
// the node answered, it will serve once unsealed, and retrying the same call
// before then cannot succeed. Detect it with errors.As or IsBarrierSealed,
// never by matching message text.
type BarrierSealedError struct {
	// Message is the server's account of the refusal.
	Message string
}

// Error returns a human-readable description.
func (e *BarrierSealedError) Error() string {
	return "transport: barrier sealed: " + e.Message
}

// IsBarrierSealed reports whether err, or any error it wraps, is a
// *BarrierSealedError.
func IsBarrierSealed(err error) bool {
	var sealed *BarrierSealedError
	return errors.As(err, &sealed)
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

// WatcherLimitError indicates the node refused a watch because every
// watch.max_watchers slot it has is held. The refusal is the node's capacity,
// not the request's: the same watch opens once another closes.
type WatcherLimitError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *WatcherLimitError) Error() string {
	return "transport: watch refused: " + e.Reason
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

// UnknownDistanceMetricError is returned when a vector distance metric has no
// wire value: a name the caller passed, or a value a server answered. It is
// never read as cosine.
type UnknownDistanceMetricError struct {
	Metric string
}

// Error returns a human-readable description.
func (e *UnknownDistanceMetricError) Error() string {
	return "transport: unknown vector distance metric " + strconv.Quote(e.Metric) +
		" (want cosine, euclidean, l2, dot_product or manhattan)"
}

// ReasonVectorNamespaceNotFound is the structured-refusal reason every
// transport carries beside the message of a missing vector namespace, so a
// client rebuilds a *NamespaceNotFoundError without parsing text.
const ReasonVectorNamespaceNotFound = "VECTOR_NAMESPACE_NOT_FOUND"

// ReasonVectorDimensionMismatch is the structured-refusal reason of a vector
// dimension that differs from its namespace's, rebuilt as a
// *DimensionMismatchError.
const ReasonVectorDimensionMismatch = "VECTOR_DIMENSION_MISMATCH"

// Refusal metadata keys of the vector refusals.
const (
	vectorKeyNamespace = "namespace"
	vectorKeyExpected  = "expected"
	vectorKeyGot       = "got"
)

// Metadata encodes e as structured-refusal metadata.
func (e *NamespaceNotFoundError) Metadata() map[string]string {
	return map[string]string{vectorKeyNamespace: e.Namespace}
}

// NamespaceNotFoundFromMetadata rebuilds the error Metadata encoded.
func NamespaceNotFoundFromMetadata(md map[string]string) *NamespaceNotFoundError {
	return &NamespaceNotFoundError{Namespace: md[vectorKeyNamespace]}
}

// Metadata encodes e as structured-refusal metadata.
func (e *DimensionMismatchError) Metadata() map[string]string {
	return map[string]string{
		vectorKeyExpected: strconv.Itoa(e.Expected),
		vectorKeyGot:      strconv.Itoa(e.Got),
	}
}

// DimensionMismatchFromMetadata rebuilds the error Metadata encoded. The
// reason, not the metadata, says the dimension differs, so a missing or
// malformed number is left zero rather than losing the classification.
func DimensionMismatchFromMetadata(md map[string]string) *DimensionMismatchError {
	expected, err := strconv.Atoi(md[vectorKeyExpected])
	if err != nil {
		expected = 0
	}
	got, err := strconv.Atoi(md[vectorKeyGot])
	if err != nil {
		got = 0
	}
	return &DimensionMismatchError{Expected: expected, Got: got}
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

// UserNotFoundError is returned when a user lookup does not resolve — by id,
// username, email or certificate SPKI thumbprint. For a lookup by id, ID
// carries the numeric identifier and Lookup is empty; for any other key,
// Lookup carries a descriptor of the key that failed and ID is zero.
//
// It is the ONE type for this ONE condition across the whole SDK surface:
// every transport that can carry it returns it, and the user SERVICE returns
// it too — pkg/user.UserNotFoundError is an alias of this type (go-qrdb#343),
// so a consumer holding a UserServicer in-process and a consumer calling over
// a wire classify a missing user with the same errors.As against
// sdk.UserNotFoundError. There is no second type to match and no translation
// step to forget.
//
// It is declared here rather than in pkg/user because sdk/go is its own Go
// module and this package imports nothing from pkg/: root packages import
// sdk/go/transport, so the alias runs in that direction. The message names
// the user subsystem, not this package, because the condition originates
// there and the same text is produced whichever layer returns it.
type UserNotFoundError struct {
	// ID is the numeric user id that was not found. Zero when the lookup was
	// not by id (see Lookup).
	ID uint64

	// Lookup is a descriptor of a non-id lookup that failed (e.g.
	// "tenant/username" or "cert_spki/<thumbprint>"). Empty for id lookups.
	Lookup string
}

// Error returns a human-readable description.
func (e *UserNotFoundError) Error() string {
	if e.Lookup != "" {
		return "user: not found: " + e.Lookup
	}
	return "user: not found: " + strconv.FormatUint(e.ID, 10)
}

// UserLookupKey formats the descriptor UserNotFoundError.Lookup carries for a
// tenant-scoped lookup, so every transport spells the same failed lookup the
// same way rather than each inventing a format. An empty tenantID selects the
// server's default tenant and is omitted rather than rendered as an empty path
// segment.
//
// A remote client knows only the tenant it PASSED, so when it passes none this
// descriptor names the key alone while the embedded transport — which sees the
// server's resolved tenant — names both. That difference is information the
// wire does not carry, not a formatting inconsistency: match on the type and
// on ID, and treat Lookup as a diagnostic.
func UserLookupKey(tenantID, key string) string {
	if tenantID == "" {
		return key
	}
	return tenantID + "/" + key
}

// UserConcurrentUpdateError reports that another update committed to the same
// user record between a call's read and its write, so the call did not apply.
// Nothing is wrong: the caller lost a race and its view of the record is
// stale, and re-reading and retrying is the correct response.
//
// Like UserNotFoundError it is the ONE type for this condition across the SDK
// surface: pkg/user.ConcurrentUpdateError is an alias of it (go-qrdb#938), the
// user service returns it in-process, and every remote transport rebuilds it
// from the ReasonUserConcurrentUpdate structured refusal. Through As it is
// also a *QRDBError with code ErrConflict and Retry set, so errors.IsRetryable,
// errors.IsErrorCode and pkg/retry's default classifier treat it as the
// retryable conflict it is on every transport.
type UserConcurrentUpdateError struct {
	// ID is the numeric id of the user whose update lost the race.
	ID uint64
}

// Error returns a human-readable description of the lost race.
func (e *UserConcurrentUpdateError) Error() string {
	return "user: concurrent update to user " + strconv.FormatUint(e.ID, 10) + "; re-read and retry"
}

// As lets errors.As match a **dberrors.QRDBError, presenting the lost race as
// a retryable ErrConflict, so a caller classifies it by code and retries it
// without naming this type.
func (e *UserConcurrentUpdateError) As(target any) bool {
	qe, ok := target.(**dberrors.QRDBError)
	if !ok {
		return false
	}
	*qe = &dberrors.QRDBError{Code: dberrors.ErrConflict, Op: "user.update", Err: e, Retry: true}
	return true
}

// ReasonUserConcurrentUpdate is the structured-refusal reason every transport
// carries beside the message of a *UserConcurrentUpdateError, so a client
// rebuilds the typed error without parsing text.
const ReasonUserConcurrentUpdate = "USER_CONCURRENT_UPDATE"

// userConcurrentUpdateKeyUserID is the refusal metadata key of
// UserConcurrentUpdateError.ID.
const userConcurrentUpdateKeyUserID = "user_id"

// Metadata encodes e as structured-refusal metadata.
func (e *UserConcurrentUpdateError) Metadata() map[string]string {
	return map[string]string{userConcurrentUpdateKeyUserID: strconv.FormatUint(e.ID, 10)}
}

// UserConcurrentUpdateFromMetadata rebuilds the error Metadata encoded. The
// reason, not the metadata, says the update lost a race, so a missing or
// malformed user id leaves ID zero rather than losing the classification.
func UserConcurrentUpdateFromMetadata(md map[string]string) *UserConcurrentUpdateError {
	id, err := strconv.ParseUint(md[userConcurrentUpdateKeyUserID], 10, 64)
	if err != nil {
		id = 0
	}
	return &UserConcurrentUpdateError{ID: id}
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

// BackupDecryptionFailedError is returned when a backup's envelope cannot
// be processed: an unregistered key-wrap provider (create or restore), a
// bad key reference, or the wrong KEK/a tampered wrapped DEK. These are
// deliberately not distinguished from each other -- see
// service.BackupDecryptionFailedError, which this mirrors. A tampered
// SHARD's own ciphertext is a DIFFERENT error,
// *BackupVerificationFailedError wrapping *BackupShardIntegrityError --
// checked at a different layer (the shard's own AEAD tag, not the KEK or
// the wrapped DEK) and never collapsed into this one.
type BackupDecryptionFailedError struct {
	BackupID string
	Err      error
}

// Error returns a human-readable description.
func (e *BackupDecryptionFailedError) Error() string {
	msg := "transport: backup decryption failed"
	if e.BackupID != "" {
		msg += " for " + e.BackupID
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *BackupDecryptionFailedError) Unwrap() error {
	return e.Err
}

// BackupPlaintextRestoreNotAllowedError is returned when a backup's
// stored state is plaintext and the caller did not set
// RestoreRequest.AllowPlaintext -- mirrors
// service.BackupPlaintextRestoreNotAllowedError.
type BackupPlaintextRestoreNotAllowedError struct {
	BackupID string
}

// Error returns a human-readable description.
func (e *BackupPlaintextRestoreNotAllowedError) Error() string {
	return "transport: backup " + e.BackupID + " is stored as plaintext; restoring it requires an explicit opt-in (allow_plaintext)"
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
