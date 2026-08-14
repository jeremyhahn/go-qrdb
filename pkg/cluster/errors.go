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

package cluster

import (
	"strconv"
)

// QuorumError is returned when quorum cannot be established.
type QuorumError struct {
	Have int
	Need int
}

// Error returns a human-readable description of the quorum failure.
func (e *QuorumError) Error() string {
	return "bootstrap: insufficient peers for quorum (have " +
		strconv.Itoa(e.Have) + ", need " + strconv.Itoa(e.Need) + ")"
}

// Error is returned for general bootstrap failures.
type Error struct {
	Phase string
	Err   error
}

// Error returns a human-readable description of the bootstrap failure.
func (e *Error) Error() string {
	return "bootstrap: " + e.Phase + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *Error) Unwrap() error {
	return e.Err
}

// InvalidAddressError is returned when a peer address fails host:port validation.
type InvalidAddressError struct {
	Address string
	Reason  string
}

// Error returns a human-readable description of the invalid address.
func (e *InvalidAddressError) Error() string {
	return "bootstrap: invalid peer address " + e.Address + ": " + e.Reason
}

// DuplicateAddressError is returned when two different NodeIDs share
// the same address.
type DuplicateAddressError struct {
	Address string
	NodeID1 uint64
	NodeID2 uint64
}

// Error returns a human-readable description of the duplicate address.
func (e *DuplicateAddressError) Error() string {
	return "bootstrap: duplicate address " + e.Address +
		" for NodeIDs " + strconv.FormatUint(e.NodeID1, 10) +
		" and " + strconv.FormatUint(e.NodeID2, 10)
}

// PeerParseError is returned when a peer specification string cannot be
// parsed. The expected format is "id=address".
type PeerParseError struct {
	// Input is the malformed peer specification.
	Input string
}

// Error returns a human-readable description of the parse failure.
func (e *PeerParseError) Error() string {
	return "cluster: invalid peer format " + strconv.Quote(e.Input) + ", expected id=address"
}

// UnknownDiscoveryError is returned when Options.Method names a discovery
// mechanism that is not registered.
type UnknownDiscoveryError struct {
	// Name is the unrecognized discovery method.
	Name string
}

// Error returns a human-readable description of the unknown method.
func (e *UnknownDiscoveryError) Error() string {
	return "cluster: unknown discovery method " + strconv.Quote(e.Name)
}

// MissingFlagError is returned when a discovery method requires an input
// the caller did not supply. Flag identifies the missing field using the
// caller's vocabulary (a CLI flag name for the daemon, an option/field
// name for embedders).
type MissingFlagError struct {
	// Flag is the identifier of the missing required input.
	Flag string
}

// Error returns a human-readable description of the missing input.
func (e *MissingFlagError) Error() string {
	return "cluster: required discovery input not set: " + e.Flag
}

// NoPeersError is returned when static discovery yields no members and no
// single-node fallback applies.
type NoPeersError struct{}

// Error returns a human-readable description of the empty-peer condition.
func (e *NoPeersError) Error() string {
	return "cluster: no peers configured for static discovery"
}

// UnknownAPIProtocolError is returned when an advertised (or requested) API
// protocol is not one of cluster.APIProtocols.
type UnknownAPIProtocolError struct {
	// Name is the unrecognized protocol name.
	Name string
}

// Error returns a human-readable description of the unknown protocol.
func (e *UnknownAPIProtocolError) Error() string {
	return "cluster: unknown api protocol " + strconv.Quote(e.Name)
}

// EmptyAPIEndpointError is returned when a member advertises a known protocol
// with an empty address. Publishing an empty address is worse than publishing
// nothing: a consumer cannot distinguish it from "not served".
type EmptyAPIEndpointError struct {
	// Protocol is the protocol whose address was empty.
	Protocol APIProtocol
}

// Error returns a human-readable description of the empty endpoint.
func (e *EmptyAPIEndpointError) Error() string {
	return "cluster: empty api endpoint for protocol " + strconv.Quote(string(e.Protocol))
}

// MembershipError is a membership manager configuration or state error.
type MembershipError struct {
	// Reason describes the failure.
	Reason string
}

// Error returns a human-readable description.
func (e *MembershipError) Error() string {
	return "cluster: membership: " + e.Reason
}

// MembershipCauseError is a membership error wrapping an underlying cause.
type MembershipCauseError struct {
	// Reason describes the failure.
	Reason string

	// Cause is the underlying error.
	Cause error
}

// Error returns a human-readable description.
func (e *MembershipCauseError) Error() string {
	return "cluster: membership: " + e.Reason + ": " + e.Cause.Error()
}

// Unwrap returns the underlying cause.
func (e *MembershipCauseError) Unwrap() error {
	return e.Cause
}
