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

package etcd

// StartError is returned when the etcd gRPC server fails to start.
type StartError struct {
	Addr string
	Err  error
}

// Error returns a human-readable description.
func (e *StartError) Error() string {
	return "etcd: failed to start server on " + e.Addr + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *StartError) Unwrap() error { return e.Err }

// KeyNotFoundError is returned when an etcd Range finds no keys.
type KeyNotFoundError struct {
	Key string
}

// Error returns a human-readable description.
func (e *KeyNotFoundError) Error() string {
	return "etcd: key not found: " + e.Key
}

// InvalidRequestError is returned when an etcd request is malformed.
type InvalidRequestError struct {
	Detail string
}

// Error returns a human-readable description.
func (e *InvalidRequestError) Error() string {
	return "etcd: invalid request: " + e.Detail
}

// AuthNotEnabledError is returned for all Auth service RPCs.
type AuthNotEnabledError struct{}

// Error returns a human-readable description.
func (e *AuthNotEnabledError) Error() string {
	return "etcd: authentication is not enabled"
}
