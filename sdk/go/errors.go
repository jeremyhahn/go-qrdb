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

package sdk

import "strconv"

// ClientError is returned when a client-level operation fails.
type ClientError struct {
	Op  string
	Err error
}

// Error returns a human-readable description.
func (e *ClientError) Error() string {
	return "sdk: " + e.Op + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ClientError) Unwrap() error {
	return e.Err
}

// ClientClosedError is returned when an operation is attempted on a
// closed client.
type ClientClosedError struct{}

// Error returns a human-readable description.
func (e *ClientClosedError) Error() string {
	return "sdk: client is closed"
}

// NilTransportError is returned when NewClient is called with a nil
// transport.
type NilTransportError struct{}

// Error returns a human-readable description.
func (e *NilTransportError) Error() string {
	return "sdk: transport must not be nil"
}

// ShardNotFoundError is returned by Cluster() operations when the requested
// shard is not present in the node's membership view.
type ShardNotFoundError struct {
	ShardID uint64
}

// Error returns a human-readable description.
func (e *ShardNotFoundError) Error() string {
	return "sdk: shard not found in membership view: " + strconv.FormatUint(e.ShardID, 10)
}

// NoLeaderError is returned by leader-directed Cluster() operations when the
// shard currently has no elected leader. It is distinct from
// LeaderEndpointUnknownError: here there is nothing to address at all, so the
// caller should retry after an election rather than fall back.
type NoLeaderError struct {
	ShardID uint64
}

// Error returns a human-readable description.
func (e *NoLeaderError) Error() string {
	return "sdk: no leader elected for shard " + strconv.FormatUint(e.ShardID, 10)
}

// LeaderEndpointUnknownError is returned by LeaderEndpoint when the shard has a
// leader but that leader advertises no address for the requested protocol —
// because it predates API advertisement, was configured without an advertise
// address, or genuinely does not serve that protocol. Callers treat it as
// "fall back to today's behavior", not as a hard failure.
type LeaderEndpointUnknownError struct {
	ShardID   uint64
	ReplicaID uint64
	Protocol  APIProtocol
}

// Error returns a human-readable description.
func (e *LeaderEndpointUnknownError) Error() string {
	return "sdk: leader replica " + strconv.FormatUint(e.ReplicaID, 10) +
		" of shard " + strconv.FormatUint(e.ShardID, 10) +
		" advertises no " + string(e.Protocol) + " api endpoint"
}
