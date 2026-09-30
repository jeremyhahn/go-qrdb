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

// Package clustertls re-exports go-qrdb's cluster-TLS material toolkit so SDK
// consumers (go-xkms, go-quicbaas) that compose qrdbd nodes can generate and
// persist cluster TLS material without importing the internal pkg/clustertls
// path. All public types and constructors are aliases — there is no logic in
// this file.
//
// See github.com/jeremyhahn/go-qrdb/pkg/clustertls for the canonical
// documentation.
package clustertls

import clustertls "github.com/jeremyhahn/go-qrdb/pkg/clustertls"

// Default validity durations for auto-generated certificates.
const (
	// CAValidity is the default validity for auto-generated CAs.
	CAValidity = clustertls.CAValidity
	// NodeValidity is the default validity for auto-generated node leaves.
	NodeValidity = clustertls.NodeValidity
	// ClientValidity is the default validity for auto-generated client certs.
	ClientValidity = clustertls.ClientValidity
)

// Material holds the raw PEM-encoded certificate and key bytes for a cluster
// CA + node leaf certificate pair.
type Material = clustertls.Material

// GenerateError is returned when TLS certificate generation fails.
type GenerateError = clustertls.GenerateError

// LoadError is returned when loading TLS certificate material from a file fails.
type LoadError = clustertls.LoadError

// KeyError is returned when parsing certificate or key material fails.
type KeyError = clustertls.KeyError

// WriteError is returned when persisting certificate material to disk fails.
type WriteError = clustertls.WriteError

// PartialMaterialError is returned by LoadFromDir when some but not all of the
// required cluster-TLS files are present (an interrupted write or corrupted
// directory).
type PartialMaterialError = clustertls.PartialMaterialError

// ErrNoCertsInPEM is returned when a PEM file or buffer contains no valid
// certificate blocks.
var ErrNoCertsInPEM = clustertls.ErrNoCertsInPEM

// LocalSANs discovers local DNS names and interface IPs suitable for
// certificate Subject Alternative Names.
var LocalSANs = clustertls.LocalSANs

// GenerateCA creates a self-signed ECDSA P-256 CA with the given validity,
// returning the PEM material plus the parsed cert and key for signing leaves.
var GenerateCA = clustertls.GenerateCA

// GenerateNodeCert creates an ECDSA P-256 node leaf signed by the given CA,
// with SANs for all local hostnames and interface IPs and an optional SPIFFE
// node URI SAN.
var GenerateNodeCert = clustertls.GenerateNodeCert

// GenerateClientCert creates an ECDSA P-256 client-auth-only certificate signed
// by the given CA, with the supplied CN and OU.
var GenerateClientCert = clustertls.GenerateClientCert

// GenerateCABundle creates an ephemeral ECDSA P-256 CA and a leaf certificate
// signed by that CA, returning the raw PEM material. nodeURI is an optional
// SPIFFE node URI SAN.
var GenerateCABundle = clustertls.GenerateCABundle

// CertificatesOnly returns the CERTIFICATE blocks of a PEM input and nothing
// else; private-key blocks are dropped. See clustertls.CertificatesOnly.
var CertificatesOnly = clustertls.CertificatesOnly

// IssueLeafForKey signs a node leaf certificate for a caller-supplied public
// key (e.g. a hardware-resident key whose private key never leaves the device)
// using the given CA cert+key PEM. The private key is NOT generated here;
// the caller owns it and its crypto.Signer. nodeURI is an optional SPIFFE
// node URI SAN.
var IssueLeafForKey = clustertls.IssueLeafForKey

// LoadFromDir attempts to load TLS certificate material from the standard file
// names in dataDir (ca.pem, node-cert.pem, node-key.pem). When all required
// files are absent, (nil, nil) is returned; when some but not all are present a
// *PartialMaterialError is returned.
var LoadFromDir = clustertls.LoadFromDir

// LoadCAFromDir loads ca.pem and ca-key.pem from dataDir. Returns
// (nil, nil, nil) when either file is absent (no cached CA). Unlike
// LoadFromDir, this function does not require node-cert.pem or node-key.pem.
var LoadCAFromDir = clustertls.LoadCAFromDir

// LoadFromFiles loads TLS certificate material from the three named PEM files.
// Returns a *LoadError wrapping the underlying OS error on read failure.
var LoadFromFiles = clustertls.LoadFromFiles

// PersistMaterial writes TLS material to standard file names in dataDir
// (ca.pem, node-cert.pem, node-key.pem, and optionally ca-key.pem). Each file
// is written atomically.
var PersistMaterial = clustertls.PersistMaterial

// PersistCA writes only ca.pem and ca-key.pem to dataDir. Used when the node
// leaf is issued separately (caller-owned key) so there is no node-key.pem to
// persist. Each file is written atomically.
var PersistCA = clustertls.PersistCA
