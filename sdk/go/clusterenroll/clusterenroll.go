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

// Package clusterenroll re-exports go-qrdb's cluster-TLS enrollment
// orchestration so SDK consumers (go-xkms, go-quicbaas) that compose qrdbd
// nodes can resolve and advertise cluster-TLS material without importing the
// internal pkg/clusterenroll path. All public types and constructors are
// aliases — there is no logic in this file.
//
// See github.com/jeremyhahn/go-qrdb/pkg/clusterenroll for the canonical
// documentation.
package clusterenroll

import (
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	clusterenroll "github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
)

// Default values for the Noise bootstrap server.
const (
	// DefaultNoisePort is the default TCP port for the Noise NK bootstrap
	// server used for peer enrollment.
	DefaultNoisePort = clusterenroll.DefaultNoisePort

	// DefaultTimeout is the per-method timeout for bootstrap fetch operations.
	DefaultTimeout = clusterenroll.DefaultTimeout

	// NoiseKeyFile is the filename used to persist the Noise static key in
	// the data directory.
	NoiseKeyFile = clusterenroll.NoiseKeyFile
)

// Options holds the parameters for cluster-TLS enrollment orchestration.
type Options = clusterenroll.Options

// BootstrapOptions configures the truststrap join methods used when this node
// is joining an existing cluster (path 4).
type BootstrapOptions = clusterenroll.BootstrapOptions

// AdvertiseOptions configures the Noise NK bootstrap server started on the
// first node of a multi-node cluster (path 5). NoiseHost is required: the
// enrollment listener is reached by joining nodes before they hold any
// credential, so where it binds is the caller's decision to state, not this
// package's to assume.
type AdvertiseOptions = clusterenroll.AdvertiseOptions

// Advertiser holds the running Noise NK bootstrap server started on the first
// node for peer enrollment. It is started on every boot of a node whose
// recorded enrollment role is RoleFirstNode, not only the boot that generated
// the cluster CA.
type Advertiser = clusterenroll.Advertiser

// Role is the enrollment role a node was started in, decided on the run that
// first put cluster CA material in the data directory and recorded in
// RoleFile. It answers the one question the material itself cannot: whether
// this node owes the cluster an enrollment listener.
type Role = clusterenroll.Role

const (
	// RoleFirstNode generated the cluster CA and is the node peers enroll
	// against. It runs an enrollment listener on every boot.
	RoleFirstNode = clusterenroll.RoleFirstNode

	// RoleSingleNode generated its own CA and has no peers to enroll.
	RoleSingleNode = clusterenroll.RoleSingleNode

	// RoleJoined fetched the cluster CA from an existing cluster; enrollment
	// is served by the first node, not by this one.
	RoleJoined = clusterenroll.RoleJoined
)

// RoleFile is the filename, inside the data directory, that records the
// enrollment role.
const RoleFile = clusterenroll.RoleFile

// IncompleteFirstNodeMaterialError is returned when a node recorded as
// RoleFirstNode has cached CA material that cannot back an enrollment
// listener. It names that node's own data directory, because the symptom
// otherwise appears on the JOINING node — an advertiser with no CA private key
// answers and serves an unusable bundle.
type IncompleteFirstNodeMaterialError = clusterenroll.IncompleteFirstNodeMaterialError

// UnknownRoleError is returned when the role file exists but holds a value
// this package did not write. It fails closed rather than defaulting: one
// default opens an enrollment listener nobody asked for and the other
// withholds one that was asked for.
type UnknownRoleError = clusterenroll.UnknownRoleError

// MissingRoleError is returned when the data directory holds cluster CA
// material but no role file, so the node cannot know whether it owes the
// cluster an enrollment listener. The node REFUSES TO START: a first node in
// this state is silently unjoinable for as long as it runs, and the only thing
// that ever reports it is a peer's join failing on a different machine. The
// message names the file to write, the three values it accepts, the restart,
// and the caution that holding ca-key.pem is not evidence of being the first
// node — a joined node holds it too.
type MissingRoleError = clusterenroll.MissingRoleError

// FetchError is returned when fetching the CA bundle from a bootstrap server
// fails.
type FetchError = clusterenroll.FetchError

// JoinError is returned when the overall bootstrap join process fails after
// a successful CA-bundle fetch.
type JoinError = clusterenroll.JoinError

// ServerError is returned when starting or operating a bootstrap server fails.
type ServerError = clusterenroll.ServerError

// MissingBindHostError is returned by StartAdvertiser and
// StartAnchorAdvertiser when AdvertiseOptions.NoiseHost is empty. A bind host
// is a containment decision and the package will not invent one.
type MissingBindHostError = clusterenroll.MissingBindHostError

// MissingAdminPortError is returned by ResolveServerURL when a bootstrap
// server address must be completed with the admin port and none is
// configured. The URL is not built from a guessed port.
type MissingAdminPortError = clusterenroll.MissingAdminPortError

// ExplicitFilesError is returned by ResolveCA when TLS*File options are set.
// ResolveCA resolves only the cluster CA signing key; explicit file options
// supply a pre-issued node cert/key pair, which is incompatible.
type ExplicitFilesError = clusterenroll.ExplicitFilesError

// Resolve implements the 5-path cluster-TLS enrollment strategy and returns
// the raw PEM material needed for mutual TLS. The returned Advertiser is
// non-nil for a node whose recorded enrollment role is RoleFirstNode, on the
// boot that generates the cluster CA and on every later boot that loads it
// from cache, and must be stopped during graceful shutdown.
var Resolve = clusterenroll.Resolve

// ResolveCA resolves the cluster CA material (cert + key PEM) WITHOUT
// generating a node leaf — for consumers that supply their own node key (e.g.
// hardware-resident) and issue their own leaf via clustertls.IssueLeafForKey.
// The returned Advertiser is non-nil for a node whose recorded enrollment role
// is RoleFirstNode, on the boot that generates the cluster CA and on every
// later boot that loads it from cache, and must be stopped during graceful
// shutdown.
var ResolveCA = clusterenroll.ResolveCA

// Join fetches the cluster CA bundle from an existing node via truststrap,
// generates a leaf certificate signed by that CA, and persists everything to
// opts.DataDir.
var Join = clusterenroll.Join

// NodeURI builds the SPIFFE node identity URI
// (spiffe://<trustDomain>/node/<nodeID>) stamped into cluster node
// certificates. Consumers that supply their own node key and issue their own
// leaf via clustertls.IssueLeafForKey use this to match the canonical SAN
// format so cluster key-transport SAN checks succeed.
var NodeURI = spiffe.NodeURI

// StartAdvertiser starts the Noise NK bootstrap server so that joining nodes
// can securely fetch the cluster CA bundle.
var StartAdvertiser = clusterenroll.StartAdvertiser

// ResumeAdvertiser starts the enrollment listener for a node whose recorded
// enrollment role is RoleFirstNode, on a boot that loaded its cluster CA from
// cache rather than generating it. It returns (nil, nil) for a role that
// serves no enrollment, and *MissingRoleError for a data directory that holds
// cluster CA material and no recorded role, which fails the boot.
//
// Consumers that keep their own cached-material branch above Resolve -- as
// cmd/qrdbd does, to preserve its own X.509 error types -- must call this from
// that branch. Otherwise their cached boots never reach Resolve's resume and a
// first node comes back with no enrollment listener.
var ResumeAdvertiser = clusterenroll.ResumeAdvertiser

// StartAnchorAdvertiser starts the Noise NK bootstrap server serving an
// anchor-only bundle (trust-anchor certificates, never a private key) for
// attestation-based enrollment modes where joining nodes obtain their node
// certificate from a certificate authority instead of self-issuing.
var StartAnchorAdvertiser = clusterenroll.StartAnchorAdvertiser

// FetchAnchor fetches the trust-anchor bundle from an existing cluster node
// via truststrap and validates it as anchor-only material (fails closed on
// private-key material).
var FetchAnchor = clusterenroll.FetchAnchor

// ValidateAnchorBundle verifies a bundle contains at least one certificate
// and no private-key material.
var ValidateAnchorBundle = clusterenroll.ValidateAnchorBundle

// AnchorError is returned when an anchor-only bundle fails validation.
type AnchorError = clusterenroll.AnchorError

// ResolveServerURL derives the HTTPS URL of the bootstrap server from
// BootstrapOptions. It returns an empty URL and a nil error when no address
// source is configured, and *MissingAdminPortError when one is configured but
// the admin port needed to complete it is not.
var ResolveServerURL = clusterenroll.ResolveServerURL

// ExtractHostFromPeer extracts the host portion from a peer specification in
// the format "id=host:port".
var ExtractHostFromPeer = clusterenroll.ExtractHostFromPeer

// NoiseAvailable reports whether the Noise NK enrollment method may be used in
// this process. It is false in FIPS 140-3 mode, where Noise's X25519 handshake
// is not an approved key-agreement scheme: a first node then advertises over
// the approved methods only (SPKI-pinned TLS, DANE, direct HTTPS against
// /v1/ca/bootstrap) and a join configured with Noise alone is refused.
//
// Consumers call this to describe the enrollment surface they are about to
// stand up. Reading crypto/fips140 directly gives a different answer on
// GODEBUG=fips140=on, where the module permits X25519 and this stack still
// declines it.
var NoiseAvailable = clusterenroll.NoiseAvailable

// NoiseFIPSUnavailableError is returned when an enrollment operation cannot
// proceed without the Noise NK method and the process is in FIPS 140-3 mode.
// Its message names FIPS and the approved methods rather than the misleading
// "noise: invalid key size" the Noise library reports for a FIPS refusal.
type NoiseFIPSUnavailableError = clusterenroll.NoiseFIPSUnavailableError
