// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package delegation

import "time"

// GrantError reports a grant field that is not well formed.
type GrantError struct {
	// Field names the offending field: tenant, principal or scope.
	Field string
	// Reason says what is wrong with it.
	Reason string
}

// Error implements error.
func (e *GrantError) Error() string {
	return "delegation: " + e.Field + ": " + e.Reason
}

// ExtensionError reports a delegation extension that cannot be encoded or
// is not a well-formed, critical DelegationGrant.
type ExtensionError struct {
	// Reason says what is wrong.
	Reason string
	// Err is the underlying cause, when there is one.
	Err error
}

// Error implements error.
func (e *ExtensionError) Error() string {
	msg := "delegation: extension: " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying cause.
func (e *ExtensionError) Unwrap() error { return e.Err }

// LifetimeError reports a delegation certificate valid for longer than
// MaxLifetime.
type LifetimeError struct {
	// Lifetime is the certificate's NotAfter minus its NotBefore.
	Lifetime time.Duration
}

// Error implements error.
func (e *LifetimeError) Error() string {
	return "delegation: certificate lifetime " + e.Lifetime.String() + " exceeds " + MaxLifetime.String()
}
