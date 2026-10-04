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

package keywrap

import "strings"

// KeyDirectoryError is returned by NewSoftwareProvider when the configured
// key directory cannot be opened as an *os.Root (missing, not a directory,
// no permission). It is a construction-time, server-local error -- the
// directory is an operator configuration value, never something a remote
// caller supplies -- so it carries the directory and the underlying OS
// error for the operator who reads the boot log.
type KeyDirectoryError struct {
	Dir string
	Err error
}

// Error returns a human-readable description.
func (e *KeyDirectoryError) Error() string {
	return "keywrap: key directory " + e.Dir + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *KeyDirectoryError) Unwrap() error {
	return e.Err
}

// KeyAccessError is returned by SoftwareProvider.Wrap/Unwrap when keyRef
// could not be resolved to a usable 32-byte key file: keyRef was not a bare
// file name (it contained a path separator, was empty, "." or "..", or
// otherwise named something outside the configured key directory), the
// named file does not exist or could not be opened, is not a regular file,
// or is not exactly 32 bytes long.
//
// It deliberately carries NO detail in its own Error() -- not the
// directory, not keyRef, not the underlying OS error, and not the file's
// actual length. keyRef is resolved entirely on this node, and a caller of
// Wrap/Unwrap may be relaying a value a REMOTE client supplied (the backup
// service's BackupOptions.KeyRef / RestoreOptions.DecryptionKeyRef); echoing
// any of that detail back would turn this error into a filesystem oracle --
// confirming file existence, permissions, or size to a caller probing key
// references it does not control (go-qrdb security review,
// backup-encrypt-default #B4). Full detail is written to the server's own
// log via slog at the point of failure, never returned.
type KeyAccessError struct{}

// Error returns a human-readable description with no caller-controlled or
// filesystem-derived detail. See the type's own doc comment for why.
func (e *KeyAccessError) Error() string {
	return "keywrap: key reference could not be resolved to a usable key; see the server log for detail"
}

// ProviderRegisterError is returned by Register when the supplied factory
// or id is malformed (nil factory, empty id).
type ProviderRegisterError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *ProviderRegisterError) Error() string {
	return "keywrap: provider register failed: " + e.Reason
}

// ProviderAlreadyRegisteredError is returned by Register when id is
// already registered. Callers wanting replacement semantics must
// Unregister first.
type ProviderAlreadyRegisteredError struct {
	ID string
}

// Error returns a human-readable description.
func (e *ProviderAlreadyRegisteredError) Error() string {
	return "keywrap: provider already registered: " + e.ID
}

// ProviderNotRegisteredError is returned by LookupFactory/Lookup when the
// requested id has no registered factory. Available carries the
// lexicographically-sorted set of registered ids so callers can surface a
// useful error.
type ProviderNotRegisteredError struct {
	ID        string
	Available []string
}

// Error returns a human-readable description.
func (e *ProviderNotRegisteredError) Error() string {
	return "keywrap: provider not registered: " + e.ID + " (available: " + joinStrings(e.Available) + ")"
}

// WrapFailedError is returned when constructing the AEAD cipher over an
// otherwise-valid key fails, or when generating a fresh nonce fails. Both
// are operational failures (a broken entropy source, an unexpected cipher
// error), never a caller-supplied key or data problem.
type WrapFailedError struct {
	Err error
}

// Error returns a human-readable description.
func (e *WrapFailedError) Error() string {
	return "keywrap: wrap failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *WrapFailedError) Unwrap() error {
	return e.Err
}

// UnwrapFailedError is returned by Provider.Unwrap when AEAD authentication
// fails -- either the KEK does not match the one Wrap used, or wrapped was
// tampered with. The two causes are deliberately NOT distinguished: a
// caller given "wrong key" versus "tampered" as separate outcomes could use
// that distinction to probe for the correct key one attempt at a time.
type UnwrapFailedError struct{}

// Error returns a human-readable description.
func (e *UnwrapFailedError) Error() string {
	return "keywrap: unwrap failed: wrong key-encrypting key, or the wrapped data was tampered with"
}

// joinStrings renders a slice of strings as a comma-separated list,
// mirroring seal.joinNames.
func joinStrings(ss []string) string {
	return strings.Join(ss, ", ")
}
