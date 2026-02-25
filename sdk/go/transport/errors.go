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
