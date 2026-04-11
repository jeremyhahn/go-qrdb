// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package api

import (
	"fmt"
	"strings"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

// =============================================================================
// Raft and Backup Error Types
// =============================================================================

// ResourceClosedError indicates an operation was attempted on a closed resource.
type ResourceClosedError struct {
	Resource string
}

// Error implements the error interface.
func (e *ResourceClosedError) Error() string {
	return e.Resource + " is closed"
}

// NewResourceClosedError creates a QRDBError for an operation on a closed resource.
func NewResourceClosedError(op, resource string, code dberrors.ErrorCode) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: code,
		Op:   op,
		Err:  &ResourceClosedError{Resource: resource},
	}
}

// ChecksumError indicates a checksum verification failure.
type ChecksumError struct {
	Subject string
}

// Error implements the error interface.
func (e *ChecksumError) Error() string {
	return e.Subject + " checksum verification failed"
}

// NewChecksumError creates a QRDBError for a checksum verification failure.
func NewChecksumError(op, subject string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrDataLoss,
		Op:   op,
		Err:  &ChecksumError{Subject: subject},
	}
}

// UnsupportedValueError indicates an unsupported value was provided for a field.
type UnsupportedValueError struct {
	Field string
	Value string
}

// Error implements the error interface.
func (e *UnsupportedValueError) Error() string {
	return "unsupported " + e.Field + ": " + e.Value
}

// NewUnsupportedValueError creates a QRDBError for an unsupported value.
func NewUnsupportedValueError(op, field, value string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnsupportedValueError{Field: field, Value: value},
	}
}

// InvalidOpcodeError indicates an invalid binary protocol opcode was received.
type InvalidOpcodeError struct {
	Opcode  byte
	Context string
}

// Error implements the error interface.
func (e *InvalidOpcodeError) Error() string {
	if e.Context != "" {
		return fmt.Sprintf("invalid opcode: 0x%02x (%s)", e.Opcode, e.Context)
	}
	return fmt.Sprintf("invalid opcode: 0x%02x", e.Opcode)
}

// NewInvalidOpcodeError creates a QRDBError for an invalid binary protocol opcode.
func NewInvalidOpcodeError(op string, opcode byte, context string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InvalidOpcodeError{Opcode: opcode, Context: context},
	}
}

// IncompleteDataError indicates binary data is incomplete or truncated.
type IncompleteDataError struct {
	Subject string
}

// Error implements the error interface.
func (e *IncompleteDataError) Error() string {
	return "incomplete data: " + e.Subject
}

// NewIncompleteDataError creates a QRDBError for incomplete binary data.
func NewIncompleteDataError(op, subject string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &IncompleteDataError{Subject: subject},
	}
}

// ShardOperationError indicates a shard-specific operation failure.
type ShardOperationError struct {
	ShardID   uint64
	Operation string
	Cause     error
}

// Error implements the error interface.
func (e *ShardOperationError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s for shard %d: %v", e.Operation, e.ShardID, e.Cause)
	}
	return fmt.Sprintf("%s for shard %d", e.Operation, e.ShardID)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ShardOperationError) Unwrap() error {
	return e.Cause
}

// NewShardOperationError creates a QRDBError for a shard-specific operation failure.
func NewShardOperationError(op string, code dberrors.ErrorCode, shardID uint64, operation string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: code,
		Op:   op,
		Err:  &ShardOperationError{ShardID: shardID, Operation: operation, Cause: cause},
	}
}

// NodeOperationError indicates a node membership operation failure.
type NodeOperationError struct {
	NodeID    uint64
	ShardID   uint64
	Operation string
	Cause     error
}

// Error implements the error interface.
func (e *NodeOperationError) Error() string {
	return fmt.Sprintf("failed to %s node %d in shard %d: %v", e.Operation, e.NodeID, e.ShardID, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *NodeOperationError) Unwrap() error {
	return e.Cause
}

// NewNodeOperationError creates a QRDBError for a node membership operation failure.
func NewNodeOperationError(op string, nodeID, shardID uint64, operation string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrRaft,
		Op:   op,
		Err:  &NodeOperationError{NodeID: nodeID, ShardID: shardID, Operation: operation, Cause: cause},
	}
}

// DeadlineRequiredError indicates a context deadline is required but not present.
type DeadlineRequiredError struct{}

// Error implements the error interface.
func (e *DeadlineRequiredError) Error() string {
	return "context must have deadline"
}

// NewDeadlineRequiredError creates a QRDBError for a missing context deadline.
func NewDeadlineRequiredError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &DeadlineRequiredError{},
	}
}

// DeadlineExceededError indicates a context deadline has already been exceeded.
type DeadlineExceededError struct{}

// Error implements the error interface.
func (e *DeadlineExceededError) Error() string {
	return "context deadline already exceeded"
}

// NewDeadlineExceededError creates a QRDBError for an exceeded context deadline.
func NewDeadlineExceededError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrDeadlineExceeded,
		Op:   op,
		Err:  &DeadlineExceededError{},
	}
}

// LengthMismatchError indicates two related collections have mismatched lengths.
type LengthMismatchError struct {
	FieldA string
	LenA   int
	FieldB string
	LenB   int
}

// Error implements the error interface.
func (e *LengthMismatchError) Error() string {
	return fmt.Sprintf("%s and %s length mismatch: %d != %d", e.FieldA, e.FieldB, e.LenA, e.LenB)
}

// NewLengthMismatchError creates a QRDBError for mismatched collection lengths.
func NewLengthMismatchError(op, fieldA string, lenA int, fieldB string, lenB int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &LengthMismatchError{FieldA: fieldA, LenA: lenA, FieldB: fieldB, LenB: lenB},
	}
}

// SnapshotOperationError indicates a snapshot operation failure.
type SnapshotOperationError struct {
	Operation string
	Cause     error
}

// Error implements the error interface.
func (e *SnapshotOperationError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("snapshot %s: %v", e.Operation, e.Cause)
	}
	return "snapshot " + e.Operation
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *SnapshotOperationError) Unwrap() error {
	return e.Cause
}

// NewSnapshotOperationError creates a QRDBError for a snapshot operation failure.
func NewSnapshotOperationError(op, operation string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrSnapshot,
		Op:   op,
		Err:  &SnapshotOperationError{Operation: operation, Cause: cause},
	}
}

// UnknownOperationError indicates an unrecognized operation was requested.
type UnknownOperationError struct {
	Operation string
}

// Error implements the error interface.
func (e *UnknownOperationError) Error() string {
	return "unknown operation: " + e.Operation
}

// NewUnknownOperationError creates a QRDBError for an unknown operation.
func NewUnknownOperationError(op, operation string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnknownOperationError{Operation: operation},
	}
}

// InvalidContextTypeError indicates an invalid type for a context parameter.
type InvalidContextTypeError struct {
	Expected string
}

// Error implements the error interface.
func (e *InvalidContextTypeError) Error() string {
	return "invalid context type, expected " + e.Expected
}

// NewInvalidContextTypeError creates a QRDBError for an invalid context type.
func NewInvalidContextTypeError(op, expected string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &InvalidContextTypeError{Expected: expected},
	}
}

// CanceledError indicates an operation was canceled.
type CanceledError struct {
	Operation string
}

// Error implements the error interface.
func (e *CanceledError) Error() string {
	return e.Operation + " canceled"
}

// NewCanceledError creates a QRDBError for a canceled operation.
func NewCanceledError(op, operation string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrCanceled,
		Op:   op,
		Err:  &CanceledError{Operation: operation},
	}
}

// BatchOperationError indicates a failure within a batch operation at a specific index.
type BatchOperationError struct {
	Index     int
	Operation string
	Cause     error
}

// Error implements the error interface.
func (e *BatchOperationError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("batch operation %d (%s) failed: %v", e.Index, e.Operation, e.Cause)
	}
	return fmt.Sprintf("batch operation %d has invalid type: %s", e.Index, e.Operation)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *BatchOperationError) Unwrap() error {
	return e.Cause
}

// NewBatchOperationError creates a QRDBError for a batch operation failure.
func NewBatchOperationError(op string, index int, operation string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &BatchOperationError{Index: index, Operation: operation, Cause: cause},
	}
}

// ComponentNotInitializedError indicates a required component was not initialized.
type ComponentNotInitializedError struct {
	Component string
}

// Error implements the error interface.
func (e *ComponentNotInitializedError) Error() string {
	return e.Component + " not initialized"
}

// NewComponentNotInitializedError creates a QRDBError for an uninitialized component.
func NewComponentNotInitializedError(op, component string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &ComponentNotInitializedError{Component: component},
	}
}

// WriterClosedError indicates a write was attempted on a closed writer.
type WriterClosedError struct{}

// Error implements the error interface.
func (e *WriterClosedError) Error() string {
	return "writer is closed"
}

// NewWriterClosedError creates a QRDBError for a write on a closed writer.
func NewWriterClosedError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &WriterClosedError{},
	}
}

// =============================================================================
// Additional Raft Error Types (for raft package migration)
// =============================================================================

// InvalidQueryTypeError indicates an unsupported query type was provided.
type InvalidQueryTypeError struct {
	Expected string
	Actual   string
}

// Error implements the error interface.
func (e *InvalidQueryTypeError) Error() string {
	if e.Actual != "" {
		return "invalid query type: got " + e.Actual + ", expected " + e.Expected
	}
	return "invalid query type: expected " + e.Expected
}

// NewInvalidQueryTypeError creates a QRDBError for an invalid query type.
func NewInvalidQueryTypeError(op, expected, actual string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InvalidQueryTypeError{Expected: expected, Actual: actual},
	}
}

// RequiredFieldOperationError indicates a required field is missing for an operation.
type RequiredFieldOperationError struct {
	Field     string
	Operation string
}

// Error implements the error interface.
func (e *RequiredFieldOperationError) Error() string {
	return e.Field + " is required for " + e.Operation + " operation"
}

// NewRequiredFieldOperationError creates a QRDBError for a missing required field in an operation.
func NewRequiredFieldOperationError(op, field, operation string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &RequiredFieldOperationError{Field: field, Operation: operation},
	}
}

// InvalidEntityKeyError indicates an invalid entity key was constructed.
type InvalidEntityKeyError struct {
	Operation string
}

// Error implements the error interface.
func (e *InvalidEntityKeyError) Error() string {
	return "invalid entity key in " + e.Operation + " operation"
}

// NewInvalidEntityKeyError creates a QRDBError for an invalid entity key.
func NewInvalidEntityKeyError(op, operation string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InvalidEntityKeyError{Operation: operation},
	}
}

// BatchFormatError indicates an invalid batch format.
type BatchFormatError struct {
	Message string
}

// Error implements the error interface.
func (e *BatchFormatError) Error() string {
	return "batch operations must be " + e.Message
}

// NewBatchFormatError creates a QRDBError for an invalid batch format.
func NewBatchFormatError(op, message string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &BatchFormatError{Message: message},
	}
}

// InsufficientPeersError indicates not enough peers are available.
type InsufficientPeersError struct {
	Required int
	Have     int
}

// Error implements the error interface.
func (e *InsufficientPeersError) Error() string {
	return fmt.Sprintf("insufficient peers: need %d, have %d", e.Required, e.Have)
}

// NewInsufficientPeersError creates a QRDBError for insufficient peers.
func NewInsufficientPeersError(op string, code dberrors.ErrorCode, required, have int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: code,
		Op:   op,
		Err:  &InsufficientPeersError{Required: required, Have: have},
	}
}

// DuplicateNodeIDError indicates a duplicate node ID was found.
type DuplicateNodeIDError struct {
	NodeID uint64
}

// Error implements the error interface.
func (e *DuplicateNodeIDError) Error() string {
	return fmt.Sprintf("duplicate node ID: %d", e.NodeID)
}

// NewDuplicateNodeIDError creates a QRDBError for a duplicate node ID.
func NewDuplicateNodeIDError(op string, nodeID uint64) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &DuplicateNodeIDError{NodeID: nodeID},
	}
}

// EmptyPeerAddressError indicates a peer has an empty address.
type EmptyPeerAddressError struct {
	NodeID uint64
}

// Error implements the error interface.
func (e *EmptyPeerAddressError) Error() string {
	return fmt.Sprintf("peer %d has empty address", e.NodeID)
}

// NewEmptyPeerAddressError creates a QRDBError for a peer with an empty address.
func NewEmptyPeerAddressError(op string, nodeID uint64) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &EmptyPeerAddressError{NodeID: nodeID},
	}
}

// InvalidNodeIDZeroError indicates a node ID of zero was found.
type InvalidNodeIDZeroError struct{}

// Error implements the error interface.
func (e *InvalidNodeIDZeroError) Error() string {
	return "peer has invalid node_id=0"
}

// NewInvalidNodeIDZeroError creates a QRDBError for a zero node ID.
func NewInvalidNodeIDZeroError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InvalidNodeIDZeroError{},
	}
}

// NodeNotInMembersError indicates a node ID was not found in the members list.
type NodeNotInMembersError struct {
	NodeID uint64
}

// Error implements the error interface.
func (e *NodeNotInMembersError) Error() string {
	return fmt.Sprintf("node_id %d not found in initial_members", e.NodeID)
}

// NewNodeNotInMembersError creates a QRDBError for a node not found in members.
func NewNodeNotInMembersError(op string, nodeID uint64) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &NodeNotInMembersError{NodeID: nodeID},
	}
}

// LeaderNotFoundInPeersError indicates the leader was not found in the peers list.
type LeaderNotFoundInPeersError struct {
	LeaderID uint64
}

// Error implements the error interface.
func (e *LeaderNotFoundInPeersError) Error() string {
	if e.LeaderID > 0 {
		return fmt.Sprintf("leader %d not found in peers list", e.LeaderID)
	}
	return "leader peer not found in peers list"
}

// NewLeaderNotFoundInPeersError creates a QRDBError for a leader not found in peers.
func NewLeaderNotFoundInPeersError(op string, code dberrors.ErrorCode, leaderID uint64) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: code,
		Op:   op,
		Err:  &LeaderNotFoundInPeersError{LeaderID: leaderID},
	}
}

// LeaderMismatchError indicates the observed leader does not match the expected leader.
type LeaderMismatchError struct {
	Expected uint64
	Observed uint64
}

// Error implements the error interface.
func (e *LeaderMismatchError) Error() string {
	return fmt.Sprintf("leader mismatch: expected %d, got %d", e.Expected, e.Observed)
}

// NewLeaderMismatchError creates a QRDBError for a leader mismatch.
func NewLeaderMismatchError(op string, expected, observed uint64) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &LeaderMismatchError{Expected: expected, Observed: observed},
	}
}

// NoFollowersError indicates no follower nodes were found.
type NoFollowersError struct {
	Context string
}

// Error implements the error interface.
func (e *NoFollowersError) Error() string {
	if e.Context != "" {
		return "no followers found " + e.Context
	}
	return "no followers found"
}

// NewNoFollowersError creates a QRDBError for no followers found.
func NewNoFollowersError(op, context string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &NoFollowersError{Context: context},
	}
}

// TimeoutWaitingError indicates a timeout while waiting for a condition.
type TimeoutWaitingError struct {
	Subject string
	Cause   error
}

// Error implements the error interface.
func (e *TimeoutWaitingError) Error() string {
	if e.Cause != nil {
		return "timeout waiting for " + e.Subject + ": " + e.Cause.Error()
	}
	return "timeout waiting for " + e.Subject
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *TimeoutWaitingError) Unwrap() error {
	return e.Cause
}

// NewTimeoutWaitingError creates a QRDBError for a timeout while waiting.
func NewTimeoutWaitingError(op, subject string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrTimeout,
		Op:   op,
		Err:  &TimeoutWaitingError{Subject: subject, Cause: cause},
	}
}

// ChannelNotRegisteredError indicates no channel is registered for a node.
type ChannelNotRegisteredError struct {
	NodeID uint64
}

// Error implements the error interface.
func (e *ChannelNotRegisteredError) Error() string {
	return fmt.Sprintf("no channel registered for node %d", e.NodeID)
}

// NewChannelNotRegisteredError creates a QRDBError for a missing channel registration.
func NewChannelNotRegisteredError(op string, nodeID uint64) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &ChannelNotRegisteredError{NodeID: nodeID},
	}
}

// ContextCancelledError indicates a context was cancelled during an operation.
type ContextCancelledError struct {
	Operation string
	Cause     error
}

// Error implements the error interface.
func (e *ContextCancelledError) Error() string {
	if e.Cause != nil {
		return "context cancelled while " + e.Operation + ": " + e.Cause.Error()
	}
	return "context cancelled while " + e.Operation
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ContextCancelledError) Unwrap() error {
	return e.Cause
}

// NewContextCancelledError creates a QRDBError for a cancelled context during an operation.
func NewContextCancelledError(op, operation string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrTimeout,
		Op:   op,
		Err:  &ContextCancelledError{Operation: operation, Cause: cause},
	}
}

// ProposalResultError indicates a Raft proposal received an unexpected result.
type ProposalResultError struct {
	ShardID uint64
	Result  string
}

// Error implements the error interface.
func (e *ProposalResultError) Error() string {
	return e.Result + fmt.Sprintf(" for shard %d", e.ShardID)
}

// NewProposalResultError creates a QRDBError for a proposal result error.
func NewProposalResultError(op string, code dberrors.ErrorCode, shardID uint64, result string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: code,
		Op:   op,
		Err:  &ProposalResultError{ShardID: shardID, Result: result},
	}
}

// AlreadyBootstrappedError indicates the cluster has already been bootstrapped.
type AlreadyBootstrappedError struct{}

// Error implements the error interface.
func (e *AlreadyBootstrappedError) Error() string {
	return "already bootstrapped"
}

// NewAlreadyBootstrappedError creates a QRDBError for an already bootstrapped cluster.
func NewAlreadyBootstrappedError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrAlreadyExists,
		Op:   op,
		Err:  &AlreadyBootstrappedError{},
	}
}

// RaftGroupExistsError indicates a Raft group already exists for the given shard.
type RaftGroupExistsError struct {
	ShardID uint64
}

// Error implements the error interface.
func (e *RaftGroupExistsError) Error() string {
	return fmt.Sprintf("raft group for shard %d already exists", e.ShardID)
}

// NewRaftGroupExistsError creates a QRDBError for an already existing Raft group.
func NewRaftGroupExistsError(op string, shardID uint64) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrAlreadyExists,
		Op:   op,
		Err:  &RaftGroupExistsError{ShardID: shardID},
	}
}

// RaftGroupNotFoundError indicates a Raft group was not found for the given shard.
type RaftGroupNotFoundError struct {
	ShardID uint64
}

// Error implements the error interface.
func (e *RaftGroupNotFoundError) Error() string {
	return fmt.Sprintf("raft group for shard %d not found", e.ShardID)
}

// NewRaftGroupNotFoundError creates a QRDBError for a missing Raft group.
func NewRaftGroupNotFoundError(op string, shardID uint64) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &RaftGroupNotFoundError{ShardID: shardID},
	}
}

// ConfigMultiValidationError indicates multiple configuration validation errors occurred.
type ConfigMultiValidationError struct {
	Errors []string
}

// Error implements the error interface.
func (e *ConfigMultiValidationError) Error() string {
	return fmt.Sprintf("validation failed with %d error(s):\n  - %s", len(e.Errors), strings.Join(e.Errors, "\n  - "))
}

// NewConfigMultiValidationError creates a QRDBError for multiple config validation errors.
func NewConfigMultiValidationError(op string, errs []string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &ConfigMultiValidationError{Errors: errs},
	}
}

// InvalidWriteModeError indicates an invalid write mode was specified.
type InvalidWriteModeError struct {
	Mode  string
	Cause error
}

// Error implements the error interface.
func (e *InvalidWriteModeError) Error() string {
	if e.Cause != nil {
		return "invalid write mode: " + e.Cause.Error()
	}
	return "invalid write mode: " + e.Mode
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *InvalidWriteModeError) Unwrap() error {
	return e.Cause
}

// NewInvalidWriteModeError creates a QRDBError for an invalid write mode.
func NewInvalidWriteModeError(op, mode string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InvalidWriteModeError{Mode: mode, Cause: cause},
	}
}

// UnsupportedConsistencyLevelError indicates an unsupported consistency level.
type UnsupportedConsistencyLevelError struct {
	Level string
}

// Error implements the error interface.
func (e *UnsupportedConsistencyLevelError) Error() string {
	return "unsupported consistency level: " + e.Level
}

// NewUnsupportedConsistencyLevelError creates a QRDBError for an unsupported consistency level.
func NewUnsupportedConsistencyLevelError(op, level string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnsupportedConsistencyLevelError{Level: level},
	}
}

// NoLeaderAvailableError indicates no leader is available for a shard.
type NoLeaderAvailableError struct {
	ShardID uint64
}

// Error implements the error interface.
func (e *NoLeaderAvailableError) Error() string {
	return fmt.Sprintf("no leader available for shard %d", e.ShardID)
}

// NewNoLeaderAvailableError creates a QRDBError for no leader available.
func NewNoLeaderAvailableError(op string, shardID uint64) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrLeaderNotAvailable,
		Op:   op,
		Err:  &NoLeaderAvailableError{ShardID: shardID},
	}
}

// PeerNotFoundError indicates a peer was not found by node ID.
type PeerNotFoundError struct {
	NodeID uint64
}

// Error implements the error interface.
func (e *PeerNotFoundError) Error() string {
	return fmt.Sprintf("peer with node_id %d not found", e.NodeID)
}

// NewPeerNotFoundError creates a QRDBError for a peer not found.
func NewPeerNotFoundError(op string, nodeID uint64) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &PeerNotFoundError{NodeID: nodeID},
	}
}

// InvalidNodeIDMembersError indicates an invalid node_id=0 in members.
type InvalidNodeIDMembersError struct{}

// Error implements the error interface.
func (e *InvalidNodeIDMembersError) Error() string {
	return "invalid node_id=0 in members"
}

// NewInvalidNodeIDMembersError creates a QRDBError for invalid node_id=0 in members.
func NewInvalidNodeIDMembersError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InvalidNodeIDMembersError{},
	}
}

// EmptyNodeAddressError indicates a node has an empty address in members.
type EmptyNodeAddressError struct {
	NodeID uint64
}

// Error implements the error interface.
func (e *EmptyNodeAddressError) Error() string {
	return fmt.Sprintf("node_id %d has empty address", e.NodeID)
}

// NewEmptyNodeAddressError creates a QRDBError for a node with an empty address.
func NewEmptyNodeAddressError(op string, nodeID uint64) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &EmptyNodeAddressError{NodeID: nodeID},
	}
}

// FieldNotStringError indicates a field value is not a string type.
type FieldNotStringError struct {
	Field  string
	Actual string
}

// Error implements the error interface.
func (e *FieldNotStringError) Error() string {
	return e.Field + " field is not a string: got " + e.Actual
}

// NewFieldNotStringError creates a QRDBError for a non-string field value.
func NewFieldNotStringError(op, field, actual string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &FieldNotStringError{Field: field, Actual: actual},
	}
}

// =============================================================================
// Node and Bootstrap Error Types
// =============================================================================

// NodeHostCreationError indicates a failure to create a Dragonboat NodeHost.
type NodeHostCreationError struct {
	Cause error
}

// Error implements the error interface.
func (e *NodeHostCreationError) Error() string {
	return "failed to create node host: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *NodeHostCreationError) Unwrap() error {
	return e.Cause
}

// NewNodeHostCreationError creates a QRDBError for a NodeHost creation failure.
func NewNodeHostCreationError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrRaft,
		Op:   op,
		Err:  &NodeHostCreationError{Cause: cause},
	}
}

// MTLSConfigError indicates mTLS is enabled but required files are missing.
type MTLSConfigError struct{}

// Error implements the error interface.
func (e *MTLSConfigError) Error() string {
	return "mTLS enabled but TLS certificate files not configured"
}

// NewMTLSConfigError creates a QRDBError for missing mTLS configuration.
func NewMTLSConfigError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &MTLSConfigError{},
	}
}

// MTLSCertReadError indicates a failure to read an mTLS certificate file.
type MTLSCertReadError struct {
	File  string
	Cause error
}

// Error implements the error interface.
func (e *MTLSCertReadError) Error() string {
	return "failed to read mTLS certificate file: " + e.File + ": " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *MTLSCertReadError) Unwrap() error {
	return e.Cause
}

// NewMTLSCertReadError creates a QRDBError for an mTLS certificate file read failure.
func NewMTLSCertReadError(op, file string, err error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &MTLSCertReadError{File: file, Cause: err},
	}
}

// GroupConfigValidationError indicates a Raft group configuration validation failure.
type GroupConfigValidationError struct {
	Cause error
}

// Error implements the error interface.
func (e *GroupConfigValidationError) Error() string {
	return "invalid group config: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *GroupConfigValidationError) Unwrap() error {
	return e.Cause
}

// NewGroupConfigValidationError creates a QRDBError for invalid group config.
func NewGroupConfigValidationError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrValidation,
		Op:   op,
		Err:  &GroupConfigValidationError{Cause: cause},
	}
}

// RaftConfigCreationError indicates a failure to create a Dragonboat Raft config.
type RaftConfigCreationError struct {
	Cause error
}

// Error implements the error interface.
func (e *RaftConfigCreationError) Error() string {
	return "failed to create raft config: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *RaftConfigCreationError) Unwrap() error {
	return e.Cause
}

// NewRaftConfigCreationError creates a QRDBError for a Raft config creation failure.
func NewRaftConfigCreationError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &RaftConfigCreationError{Cause: cause},
	}
}

// SessionError indicates a failure to get or create a Raft session.
type SessionError struct {
	Cause error
}

// Error implements the error interface.
func (e *SessionError) Error() string {
	return "failed to get session: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *SessionError) Unwrap() error {
	return e.Cause
}

// NewSessionError creates a QRDBError for a session failure.
func NewSessionError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrRaft,
		Op:   op,
		Err:  &SessionError{Cause: cause},
	}
}

// ReadOptionsValidationError indicates invalid read options were provided.
type ReadOptionsValidationError struct {
	Cause error
}

// Error implements the error interface.
func (e *ReadOptionsValidationError) Error() string {
	return "invalid read options: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ReadOptionsValidationError) Unwrap() error {
	return e.Cause
}

// NewReadOptionsValidationError creates a QRDBError for invalid read options.
func NewReadOptionsValidationError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &ReadOptionsValidationError{Cause: cause},
	}
}

// DiscoveryError indicates a failure in peer discovery.
type DiscoveryError struct {
	Cause error
}

// Error implements the error interface.
func (e *DiscoveryError) Error() string {
	return "failed to discover peers: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *DiscoveryError) Unwrap() error {
	return e.Cause
}

// NewDiscoveryError creates a QRDBError for a discovery failure.
func NewDiscoveryError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &DiscoveryError{Cause: cause},
	}
}

// ClusterDetectionError indicates a failure during cluster detection.
type ClusterDetectionError struct {
	Cause error
}

// Error implements the error interface.
func (e *ClusterDetectionError) Error() string {
	return "failed to detect existing cluster: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ClusterDetectionError) Unwrap() error {
	return e.Cause
}

// NewClusterDetectionError creates a QRDBError for a cluster detection failure.
func NewClusterDetectionError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &ClusterDetectionError{Cause: cause},
	}
}

// RaftGroupStartError indicates a failure to start a Raft group.
type RaftGroupStartError struct {
	Cause error
}

// Error implements the error interface.
func (e *RaftGroupStartError) Error() string {
	return "failed to start raft group: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *RaftGroupStartError) Unwrap() error {
	return e.Cause
}

// NewRaftGroupStartError creates a QRDBError for a Raft group start failure.
func NewRaftGroupStartError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrRaft,
		Op:   op,
		Err:  &RaftGroupStartError{Cause: cause},
	}
}

// LeaderElectionError indicates a failure during leader election.
type LeaderElectionError struct {
	Cause error
}

// Error implements the error interface.
func (e *LeaderElectionError) Error() string {
	return "leader election failed: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *LeaderElectionError) Unwrap() error {
	return e.Cause
}

// NewLeaderElectionError creates a QRDBError for a leader election failure.
func NewLeaderElectionError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrLeaderNotAvailable,
		Op:   op,
		Err:  &LeaderElectionError{Cause: cause},
	}
}

// ClusterJoinError indicates a failure to join a cluster.
type ClusterJoinError struct {
	Cause error
}

// Error implements the error interface.
func (e *ClusterJoinError) Error() string {
	return "failed to join cluster: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ClusterJoinError) Unwrap() error {
	return e.Cause
}

// NewClusterJoinError creates a QRDBError for a cluster join failure.
func NewClusterJoinError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrRaft,
		Op:   op,
		Err:  &ClusterJoinError{Cause: cause},
	}
}

// ClientContextCanceledError indicates a client's context was canceled.
type ClientContextCanceledError struct {
	Cause error
}

// Error implements the error interface.
func (e *ClientContextCanceledError) Error() string {
	return "client context canceled: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ClientContextCanceledError) Unwrap() error {
	return e.Cause
}

// NewClientContextCanceledError creates a QRDBError for a client context cancellation.
func NewClientContextCanceledError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrCanceled,
		Op:   op,
		Err:  &ClientContextCanceledError{Cause: cause},
	}
}
