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
// first node of a multi-node cluster (path 5).
type AdvertiseOptions = clusterenroll.AdvertiseOptions

// Advertiser holds the running Noise NK bootstrap server started on the first
// node for peer enrollment.
type Advertiser = clusterenroll.Advertiser

// FetchError is returned when fetching the CA bundle from a bootstrap server
// fails.
type FetchError = clusterenroll.FetchError

// JoinError is returned when the overall bootstrap join process fails after
// a successful CA-bundle fetch.
type JoinError = clusterenroll.JoinError

// ServerError is returned when starting or operating a bootstrap server fails.
type ServerError = clusterenroll.ServerError

// ExplicitFilesError is returned by ResolveCA when TLS*File options are set.
// ResolveCA resolves only the cluster CA signing key; explicit file options
// supply a pre-issued node cert/key pair, which is incompatible.
type ExplicitFilesError = clusterenroll.ExplicitFilesError

// Resolve implements the 5-path cluster-TLS enrollment strategy and returns
// the raw PEM material needed for mutual TLS. The returned Advertiser is
// non-nil only for path 5 (first node of a multi-node cluster) and must be
// stopped during graceful shutdown.
var Resolve = clusterenroll.Resolve

// ResolveCA resolves the cluster CA material (cert + key PEM) WITHOUT
// generating a node leaf — for consumers that supply their own node key (e.g.
// hardware-resident) and issue their own leaf via clustertls.IssueLeafForKey.
// The returned Advertiser is non-nil only for the first-node path and must be
// stopped during graceful shutdown.
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
// BootstrapOptions.
var ResolveServerURL = clusterenroll.ResolveServerURL

// ExtractHostFromPeer extracts the host portion from a peer specification in
// the format "id=host:port".
var ExtractHostFromPeer = clusterenroll.ExtractHostFromPeer
