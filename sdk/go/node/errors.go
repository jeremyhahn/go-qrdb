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

package node

// StorageCreateError indicates that the storage backend could not be created.
type StorageCreateError struct {
	Backend string
	Err     error
}

// Error returns a human-readable description.
func (e *StorageCreateError) Error() string {
	return "node: failed to create storage backend " + e.Backend + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *StorageCreateError) Unwrap() error { return e.Err }

// EngineCreateError indicates that the store engine could not be created.
type EngineCreateError struct {
	Engine string
	Err    error
}

// Error returns a human-readable description.
func (e *EngineCreateError) Error() string {
	return "node: failed to create engine " + e.Engine + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *EngineCreateError) Unwrap() error { return e.Err }

// ServicesCreateError indicates that the lifecycle service graph could not
// be constructed.
type ServicesCreateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ServicesCreateError) Error() string {
	return "node: failed to build services: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ServicesCreateError) Unwrap() error { return e.Err }

// ServerCreateError indicates that the multi-protocol server could not be
// started.
type ServerCreateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ServerCreateError) Error() string {
	return "node: failed to start server: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ServerCreateError) Unwrap() error { return e.Err }

// HostCreateError indicates that the quicraft host could not be created or
// the initial shard could not be started.
type HostCreateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *HostCreateError) Error() string {
	return "node: failed to create clustered host: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *HostCreateError) Unwrap() error { return e.Err }

// ConfigError indicates an invalid node configuration was supplied.
type ConfigError struct {
	Field   string
	Message string
}

// Error returns a human-readable description.
func (e *ConfigError) Error() string {
	return "node: config error: " + e.Field + " " + e.Message
}

// ClientCreateError indicates that the embedded SDK client could not be
// created.
type ClientCreateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ClientCreateError) Error() string {
	return "node: failed to create SDK client: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ClientCreateError) Unwrap() error { return e.Err }

// UnsealError indicates that the seal barrier could not be unsealed during
// node construction or a subsequent Unseal call.
type UnsealError struct {
	Err error
}

// Error returns a human-readable description.
func (e *UnsealError) Error() string {
	return "node: failed to unseal barrier: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *UnsealError) Unwrap() error { return e.Err }

// LoginWiringError indicates that the unified-login ceremony could not be
// wired into the embedded transport — typically because the
// UserAuthFactor service backing the LoginResolver could not be
// constructed from the node's KV servicer.
type LoginWiringError struct {
	Err error
}

// Error returns a human-readable description.
func (e *LoginWiringError) Error() string {
	return "node: failed to wire unified-login ceremony: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *LoginWiringError) Unwrap() error { return e.Err }

// AuthFactorWiringError indicates that the AuthFactorService could not be
// constructed from the caller-supplied FactorRegistry. This prevents a
// misconfigured enrollment/challenge/verify surface from silently
// degrading to "not supported" across the embedded transport and all
// wire protocol servers.
type AuthFactorWiringError struct {
	Err error
}

// Error returns a human-readable description.
func (e *AuthFactorWiringError) Error() string {
	return "node: failed to wire AuthFactorService from FactorRegistry: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *AuthFactorWiringError) Unwrap() error { return e.Err }
