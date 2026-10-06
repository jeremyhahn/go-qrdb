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

package main

import "strconv"

// TransportError is returned when transport construction fails.
type TransportError struct {
	Transport string
	Err       error
}

// Error returns a human-readable description.
func (e *TransportError) Error() string {
	return "qrdb: transport " + e.Transport + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TransportError) Unwrap() error {
	return e.Err
}

// ConnectError is returned when connection to the server fails.
type ConnectError struct {
	Address string
	Err     error
}

// Error returns a human-readable description.
func (e *ConnectError) Error() string {
	return "qrdb: connect to " + e.Address + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ConnectError) Unwrap() error {
	return e.Err
}

// OutputFormatError is returned when an unknown output format is specified.
type OutputFormatError struct {
	Format string
}

// Error returns a human-readable description.
func (e *OutputFormatError) Error() string {
	return "qrdb: unknown output format: " + e.Format
}

// MissingArgError is returned when a required argument is not provided.
type MissingArgError struct {
	Arg string
}

// Error returns a human-readable description.
func (e *MissingArgError) Error() string {
	return "qrdb: missing required argument: " + e.Arg
}

// InvalidArgError is returned when an argument has an invalid value.
type InvalidArgError struct {
	Arg    string
	Reason string
}

// Error returns a human-readable description.
func (e *InvalidArgError) Error() string {
	return "qrdb: invalid argument " + e.Arg + ": " + e.Reason
}

// DatabaseScopeError is returned when --database is set but the
// active transport does not support per-database scoping.
type DatabaseScopeError struct {
	Database  string
	Transport string
	Err       error
}

// Error returns a human-readable description.
func (e *DatabaseScopeError) Error() string {
	msg := "qrdb: transport " + e.Transport + " does not support scoping to database " + e.Database
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the transport's refusal.
func (e *DatabaseScopeError) Unwrap() error { return e.Err }

// InvalidLeaseIDError is returned when a lease ID argument cannot
// be parsed as an int64.
type InvalidLeaseIDError struct {
	Input string
}

// Error returns a human-readable description.
func (e *InvalidLeaseIDError) Error() string {
	return "qrdb: invalid lease ID: " + e.Input + ": must be a positive integer"
}

// InvalidAggFuncError is returned when an aggregation function name
// is not recognized.
type InvalidAggFuncError struct {
	Input string
}

// Error returns a human-readable description.
func (e *InvalidAggFuncError) Error() string {
	return "qrdb: invalid aggregation function: " + e.Input + ": must be one of avg, sum, min, max, count, percentile"
}

// FileReadError is returned when a file cannot be read.
type FileReadError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *FileReadError) Error() string {
	return "qrdb: failed to read file " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *FileReadError) Unwrap() error {
	return e.Err
}

// KeyGenerationError is returned when a local key or certificate signing
// request cannot be generated.
type KeyGenerationError struct {
	Err error
}

// Error returns a human-readable description.
func (e *KeyGenerationError) Error() string {
	return "qrdb: failed to generate a key and certificate signing request: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *KeyGenerationError) Unwrap() error {
	return e.Err
}

// MissingOutDirError is returned when a command that would generate a
// private key is given nowhere to write it.
type MissingOutDirError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *MissingOutDirError) Error() string {
	return "qrdb: --out is required: " + e.Reason
}

// FileWriteError is returned when a file cannot be written.
type FileWriteError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *FileWriteError) Error() string {
	return "qrdb: failed to write file " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *FileWriteError) Unwrap() error {
	return e.Err
}

// DirectoryCreateError is returned when a directory cannot be created.
type DirectoryCreateError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *DirectoryCreateError) Error() string {
	return "qrdb: failed to create directory " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DirectoryCreateError) Unwrap() error {
	return e.Err
}

// InvalidLogLevelError is returned when an unknown log level is specified.
type InvalidLogLevelError struct {
	Level string
}

// Error returns a human-readable description.
func (e *InvalidLogLevelError) Error() string {
	return "qrdb: unknown log level: " + e.Level
}

// TenantUnsealInputError is returned when --key is not provided to the
// tenant barrier-unseal command.
type TenantUnsealInputError struct{}

// Error returns a human-readable description.
func (e *TenantUnsealInputError) Error() string {
	return "qrdb: tenant barrier-unseal: provide --key (a shamir tenant barrier is unsealed by its holders with 'tenant barrier-quorum submit')"
}

// ShamirHolderUnsealError is returned when a Shamir barrier is asked to
// unseal from a single credential. Its shares are submitted one
// authenticated holder at a time ("qrdb barrier quorum submit", or
// OfficerBinding.SubmitShare in the desktop app).
type ShamirHolderUnsealError struct{}

// Error returns a human-readable description.
func (e *ShamirHolderUnsealError) Error() string {
	return "qrdb: a shamir barrier is unsealed by its share holders, one authenticated submission each"
}

// Base64DecodeError is returned when a base64-encoded flag value cannot
// be decoded.
type Base64DecodeError struct {
	Field string
	Err   error
}

// Error returns a human-readable description.
func (e *Base64DecodeError) Error() string {
	return "qrdb: failed to base64 decode " + e.Field + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *Base64DecodeError) Unwrap() error {
	return e.Err
}

// EmbeddedBarrierError is returned when the in-process embedded seal
// barrier fails to initialize.
type EmbeddedBarrierError struct {
	Step string
	Err  error
}

// Error returns a human-readable description.
func (e *EmbeddedBarrierError) Error() string {
	return "embedded: barrier " + e.Step + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *EmbeddedBarrierError) Unwrap() error {
	return e.Err
}

// PINsNotAllowedError is returned when --so-pin or --user-pin is
// supplied for a barrier strategy that has no PIN store (strategy
// "none" is identity passthrough; there is nothing to protect). The
// shape matches the sibling CLIs (xkmsctl, quicbaas, platformctl) per
// the canonical barrier surface.
type PINsNotAllowedError struct {
	Strategy string
	Op       string
}

// Error returns a human-readable description.
func (e *PINsNotAllowedError) Error() string {
	return "qrdb: barrier " + e.Op + ": --so-pin/--user-pin are not allowed when --barrier-strategy=" + e.Strategy
}

// UserAbortError is returned when an interactive operator declines a
// confirmation prompt (or stdin is closed before an answer is given).
// The Op field identifies the destructive operation that was canceled.
type UserAbortError struct {
	Op string
}

// Error returns a human-readable description.
func (e *UserAbortError) Error() string {
	return "qrdb: " + e.Op + " aborted by user"
}

// RestorePartialFailureError is returned when a restore operation completes
// but one or more shards failed to restore. The per-shard detail was already
// printed by printRestoreResult; this error exists solely to give the CLI a
// non-zero exit code so scripted/automation callers can detect the partial
// failure (RestoreResult.Errors is populated with a nil Go error by the
// service layer, since a partial restore is a legitimate, non-fatal outcome
// worth reporting in full rather than aborting).
type RestorePartialFailureError struct {
	BackupID    string
	FailedCount int
}

// Error returns a human-readable description.
func (e *RestorePartialFailureError) Error() string {
	return "qrdb: restore of backup " + e.BackupID + " completed with " +
		strconv.Itoa(e.FailedCount) + " failed shard(s); see output above for detail"
}

// RebalancePartialFailureError is returned when a rebalance execution
// completes but one or more shard moves failed. The per-move detail was
// already printed by printRebalanceResult; this error exists solely to give
// the CLI a non-zero exit code so scripted/automation callers can detect the
// partial failure (RebalanceResult.MovesFailed is populated with a nil Go
// error by the service layer, since a partial rebalance is a legitimate,
// non-fatal outcome worth reporting in full rather than aborting).
type RebalancePartialFailureError struct {
	FailedCount int
}

// Error returns a human-readable description.
func (e *RebalancePartialFailureError) Error() string {
	return "qrdb: rebalance completed with " + strconv.Itoa(e.FailedCount) + " failed move(s); see output above for detail"
}

// RestoreConflictsError is returned when a "kv restore" apply completes but
// leaves one or more keys unrestored because they changed since the restore
// plan was computed and --force was not set. The conflicting keys were
// already printed by printRestoreRevisionResult; this error exists solely
// to give scripted/automation callers a non-zero exit code, since a
// partially-applied restore that exits 0 reads as a complete undo.
type RestoreConflictsError struct {
	Revision   int64
	Conflicted int
}

// Error returns a human-readable description.
func (e *RestoreConflictsError) Error() string {
	return "qrdb: restore to revision " + strconv.FormatInt(e.Revision, 10) + " left " +
		strconv.Itoa(e.Conflicted) + " key(s) unrestored due to conflicts; see output above, or retry with --force"
}

// EmbeddedTransportUnavailableError is returned when the operator selects
// `--transport embedded` from a binary that is not built with an
// in-process server (i.e. the CLI). Embedded transport requires the
// desktop / GUI build which links the server side in-process.
type EmbeddedTransportUnavailableError struct{}

// Error returns a human-readable description.
func (e *EmbeddedTransportUnavailableError) Error() string {
	return "qrdb: --transport=embedded is only available in the desktop/GUI build (which links the server side in-process); use --transport=unix for a local connection over the default Unix socket"
}

// InvalidFlagValueError is returned when a repeatable flag value does not
// match the shape it declares (e.g. "role_id:tenant_id" or "key=value").
type InvalidFlagValueError struct {
	Flag  string
	Value string
	Want  string
}

// Error returns a human-readable description.
func (e *InvalidFlagValueError) Error() string {
	return "qrdb: invalid --" + e.Flag + " value " + strconv.Quote(e.Value) + ": want " + e.Want
}

// EmbeddedAuditError is returned by StartEmbedded when the compliance audit
// trail is enabled and cannot be opened. The server does not start: a node
// configured to keep a trail does not run without one.
type EmbeddedAuditError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *EmbeddedAuditError) Error() string {
	return "embedded: compliance audit trail " + strconv.Quote(e.Path) + " cannot be opened: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *EmbeddedAuditError) Unwrap() error {
	return e.Err
}

// AuditPathOutsideDataDirError is returned when the embedded server's audit
// trail path is absolute or leaves the data directory.
type AuditPathOutsideDataDirError struct {
	Path string
}

// Error returns a human-readable description.
func (e *AuditPathOutsideDataDirError) Error() string {
	return "embedded: audit trail path " + strconv.Quote(e.Path) + " must be relative to the data directory and stay inside it"
}

// ExportCertNilInputError is returned by CertBinding.Export when called
// with a nil input. Export builds its PKCS#12 archive entirely locally, so
// there is no connection to be unavailable here -- a nil input is simply a
// malformed call.
type ExportCertNilInputError struct{}

// Error returns a human-readable description.
func (e *ExportCertNilInputError) Error() string {
	return "qrdb: cert export input must not be nil"
}
