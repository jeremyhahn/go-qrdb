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

// NodeSealedError reports that a background membership write was not proposed
// because the node barrier is sealed: the replicated log cannot take an entry
// until the barrier is unsealed.
type NodeSealedError struct {
	// Op names the write that was not proposed: "register" or "deregister".
	Op string
}

// Error returns a human-readable description.
func (e *NodeSealedError) Error() string {
	return "cluster: node barrier is sealed: " + e.Op + " not proposed"
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

// MemberIdentityParseError is returned when a member-identity specification is
// not "replicaID=identity" with a non-zero replica ID, or names a replica a
// second time.
type MemberIdentityParseError struct {
	// Input is the malformed specification.
	Input string
}

// Error returns a human-readable description of the parse failure.
func (e *MemberIdentityParseError) Error() string {
	return "cluster: invalid member identity " + strconv.Quote(e.Input) +
		", expected a unique replicaID=spki:<sha256-hex> with a non-zero replica ID"
}

// MemberIdentityFormatError is returned when an identity is not the
// "spki:<64 lower-case hex digits>" form go-quicraft produces.
type MemberIdentityFormatError struct {
	// Identity is the rejected value.
	Identity string
}

// Error returns a human-readable description of the format failure.
func (e *MemberIdentityFormatError) Error() string {
	return "cluster: member identity " + strconv.Quote(e.Identity) +
		" is not of the form spki:<64 lower-case hex digits>"
}

// MemberIdentityCertificateError is returned when a node identity cannot be
// derived from certificate material.
type MemberIdentityCertificateError struct {
	// Reason describes what was wrong with the material.
	Reason string

	// Err is the underlying cause, if any.
	Err error
}

// Error returns a human-readable description of the derivation failure.
func (e *MemberIdentityCertificateError) Error() string {
	msg := "cluster: cannot derive member identity: " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying cause.
func (e *MemberIdentityCertificateError) Unwrap() error {
	return e.Err
}

// MemberIdentityMismatchError is returned when the configured identity for
// the local replica is not the identity of the certificate it presents. Every
// peer would refuse the replica's traffic, so it is refused before start.
type MemberIdentityMismatchError struct {
	// ReplicaID is the local replica.
	ReplicaID uint64

	// Configured is the identity the configuration names for it.
	Configured string

	// Derived is the identity of the certificate the replica presents.
	Derived string
}

// Error returns a human-readable description of the mismatch.
func (e *MemberIdentityMismatchError) Error() string {
	return "cluster: member identity for local replica " +
		strconv.FormatUint(e.ReplicaID, 10) + " is configured as " + e.Configured +
		" but its certificate presents " + e.Derived
}
