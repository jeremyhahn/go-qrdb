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

import (
	"strconv"
	"strings"
)

// InvalidKeyLengthError is returned when a software provider's key file is
// not exactly 32 bytes. Never truncated, padded, or hashed to size.
type InvalidKeyLengthError struct {
	Length int
}

// Error returns a human-readable description.
func (e *InvalidKeyLengthError) Error() string {
	return "keywrap: key must be exactly 32 bytes, found " + strconv.Itoa(e.Length)
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

// KeyFileError is returned when a software provider's key file cannot be
// read (missing, unreadable, a directory).
type KeyFileError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *KeyFileError) Error() string {
	return "keywrap: reading key file " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *KeyFileError) Unwrap() error {
	return e.Err
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
