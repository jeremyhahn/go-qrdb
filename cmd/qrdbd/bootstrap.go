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

package main

import (
	"context"
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
)

const (
	// noiseKeyFile is the filename used to persist the Noise static key in
	// the data directory. Aliased from pkg/clusterenroll so coverage tests
	// can reference it without importing clusterenroll directly.
	noiseKeyFile = clusterenroll.NoiseKeyFile
)

// bootstrapServers is a type alias for clusterenroll.Advertiser. It holds the
// running Noise NK bootstrap server that was started on the first node of a
// multi-node cluster for peer enrollment. The nil-safe Stop, Addr, and
// NoisePublicKey methods are provided by the Advertiser type.
type bootstrapServers = clusterenroll.Advertiser

// resolveTLS implements the 5-path TLS resolution strategy:
//
//  1. Explicit files: all three --tls-ca-file, --tls-cert-file, --tls-key-file
//     are provided. Loads directly from the specified paths.
//
//  2. Cached certs: data-dir already contains ca.pem, node-cert.pem, and
//     node-key.pem from a previous run. Loads from the data directory.
//
//  3. Single node: static discovery with only one peer. Generates a new CA
//     and node certificate, persists to data-dir.
//
//  4. Joining node: bootstrap flags are set. Fetches the CA bundle from the
//     existing cluster via go-truststrap, generates a leaf cert, persists.
//
//  5. First node: multi-node cluster without bootstrap flags. Generates a
//     new CA and node certificate, persists to data-dir, and starts bootstrap
//     servers for peer enrollment.
//
// Paths 1 and 2 are handled locally to preserve the qrdbd-specific
// *TLSLoadError / *TLSGenerateError semantics from X.509 key-pair parsing.
// Paths 3–5 delegate to pkg/clusterenroll.Resolve.
//
// The returned bootstrapServers is non-nil only for path 5 and must be
// stopped during graceful shutdown.
func resolveTLS(f *startFlags, logger *slog.Logger) (*TLSBundle, *bootstrapServers, error) {
	// Path 1: Explicit TLS files provided via flags (local: preserves
	// *TLSLoadError / *TLSGenerateError on X.509 key-pair parse failure).
	if f.tlsCAFile != "" && f.tlsCertFile != "" && f.tlsKeyFile != "" {
		logger.Info("loading TLS certificates from explicit paths",
			"ca", f.tlsCAFile,
			"cert", f.tlsCertFile,
			"key", f.tlsKeyFile,
		)
		bundle, err := loadTLSFromFiles(f.tlsCAFile, f.tlsCertFile, f.tlsKeyFile)
		if err != nil {
			return nil, nil, err
		}
		// This node's CA certificate came from the operator, not from
		// qrdb's own key material: it must never be offered to the
		// cluster CA provider as bootstrap material. See TLSBundle.ExplicitTLS.
		bundle.ExplicitTLS = true
		return bundle, nil, nil
	}

	// Path 2: Cached certificates in data-dir (local: preserves *TLSLoadError
	// / *TLSGenerateError on X.509 key-pair parse failure).
	bundle, err := loadTLSFromDir(f.dataDir)
	if err != nil {
		return nil, nil, err
	}
	if bundle != nil {
		logger.Info("loaded existing TLS certificates", "dir", f.dataDir)
		// Every restart of every node lands here, so this is where a first
		// node's enrollment listener has to come back. This branch exists to
		// keep the qrdbd-typed X.509 errors and returns BEFORE the delegation
		// to clusterenroll.Resolve below, so the resume in Resolve's own
		// cached path never runs for the daemon (go-qrdb#241).
		opts, optsErr := flagsToEnrollOptions(f)
		if optsErr != nil {
			return nil, nil, optsErr
		}
		adv, advErr := clusterenroll.ResumeAdvertiser(opts, &clustertls.Material{
			CACertPEM: bundle.CACertPEM,
			CAKeyPEM:  bundle.CAKeyPEM,
		}, logger)
		if advErr != nil {
			return nil, nil, advErr
		}
		return bundle, adv, nil
	}

	// Paths 3–5: delegate to clusterenroll.
	opts, err := flagsToEnrollOptions(f)
	if err != nil {
		return nil, nil, err
	}
	mat, adv, err := clusterenroll.Resolve(context.Background(), opts, logger)
	if err != nil {
		return nil, nil, err
	}

	tlsBundle, err := tlsBundleFromMaterial(mat)
	if err != nil {
		return nil, nil, err
	}

	return tlsBundle, adv, nil
}

// flagsToEnrollOptions converts the daemon start flags to a
// clusterenroll.Options value for paths 3–5. The member-side join token is
// resolved here, from exactly one of a file or an environment variable
// (config.ResolveJoinToken): it is never read from an inline flag or YAML
// value, so the only way this function fails is a configured source that
// cannot be read.
func flagsToEnrollOptions(f *startFlags) (*clusterenroll.Options, error) {
	joinToken, err := joinerToken(f)
	if err != nil {
		return nil, err
	}
	return &clusterenroll.Options{
		RaftAddress: f.raftAddress,
		ReplicaID:   f.replicaID,
		TrustDomain: f.clusterTrustDomain,
		DataDir:     f.dataDir,
		SingleNode:  isSingleNode(f),
		Bootstrap: clusterenroll.BootstrapOptions{
			NoiseKey:                 f.bootstrapNoiseKey,
			P256Key:                  f.bootstrapP256Key,
			NoiseAddr:                f.bootstrapNoiseAddr,
			SPKIPin:                  f.bootstrapSPKIPin,
			ServerAddr:               f.bootstrapAddr,
			DANEDNS:                  f.bootstrapDANEDNS,
			DANEDoT:                  f.bootstrapDANEDoT,
			DirectURL:                f.bootstrapDirectURL,
			DiscoveryMethod:          f.discoveryMethod,
			Peers:                    f.peers,
			DNSDomain:                f.dnsDomain,
			AdminPort:                f.adminPort,
			JoinAddr:                 f.bootstrapJoinAddr,
			JoinKey:                  f.bootstrapJoinKey,
			JoinP256Key:              f.bootstrapJoinP256Key,
			JoinHTTPURL:              f.bootstrapJoinHTTPURL,
			JoinToken:                joinToken,
			JoinApprovalPollInterval: f.bootstrapJoinApprovalPollInterval,
		},
		Advertise: clusterenroll.AdvertiseOptions{
			NoisePort: f.bootstrapNoisePort,
			NoiseHost: resolveNoiseHost(f),
			AdminPort: f.adminPort,
		},
	}, nil
}

// joinerToken resolves the member-side join admission secret from the
// configured file or environment variable. It returns "" without error
// when neither is configured -- whether because no join transport is
// configured at all, or because the operator deliberately left the token
// source unset to use the replicated approval queue instead of a token
// (see clusterenroll.BootstrapOptions.JoinToken) -- so a node joining by
// CSR never needs a token source unless it is actually using one.
func joinerToken(f *startFlags) (string, error) {
	if f.bootstrapJoinTokenFile == "" && f.bootstrapJoinTokenEnv == "" {
		return "", nil
	}
	return config.ResolveJoinToken(f.bootstrapJoinTokenFile, f.bootstrapJoinTokenEnv)
}

// resolveNoiseHost returns the bind host for this node's Noise enrollment
// listener: bootstrap.noise_host / --bootstrap-noise-host when set, else the
// API bind host. Defaulting to --host is what keeps a node contained to one
// interface from exposing enrollment on all of them (go-qrdb#167); an
// operator who wants the enrollment listener somewhere else says so.
func resolveNoiseHost(f *startFlags) string {
	if f.bootstrapNoiseHost != "" {
		return f.bootstrapNoiseHost
	}
	return f.host
}

// servesAnchor reports whether this node is a legitimate holder of qrdb's
// self-managed cluster CA (Key A) that should serve it as a trust anchor to
// joining nodes: not an explicit BYO-TLS node (whose CA is the operator's
// own, never qrdb's; see TLSBundle.ExplicitTLS) and actually holding a
// certificate. Both the per-member Noise anchor listener
// (startMemberAnchorAdvertiser) and the /v1/ca/bootstrap route are gated on
// exactly this -- replacing the old test "is this the node that generated
// the cluster CA", which left every other member unable to serve the anchor
// once that one node was gone for good.
//
// Deliberately NOT gated on single-node-ness. A self-issued-CA cluster can
// only ever FOUND with one real member -- every founder's Raft member
// identity must be known to every other founder up front, which is only
// possible for the identity a node derives from its own already-generated
// keypair, never one clusterenroll.Join would still have to generate for a
// peer that has not started yet -- so the node that generates Key A is, at
// that moment, indistinguishable from a node that intends to stay
// single-node forever. Excluding it here would make a cluster meant to grow
// by member-side CSR join (see docs/clustering/architecture.md, "Adding a
// brand-new node to a running cluster is two steps") permanently unable to
// ever serve the first of those two steps.
func servesAnchor(bundle *TLSBundle) bool {
	return bundle != nil && !bundle.ExplicitTLS && len(bundle.CACertPEM) > 0
}

// startMemberAnchorAdvertiser starts the anchor-only Noise NK listener for a
// member that resolveTLS did not already start one for (its returned
// bootstrapServers is nil): every admitted, unsealed member now advertises
// the cluster CA trust anchor, not only the one node that happened to
// generate it. It serves the IDENTICAL certificate every node already
// caches locally -- caCertPEM, the bytes clusterenroll.Resolve, Join or
// ResumeAdvertiser already persisted to ca.pem on this node -- never a
// second, independently-sourced copy of the cluster CA, and never its
// private key: clusterenroll.StartAnchorAdvertiser enforces that with the
// same ValidateAnchorBundle check the first node's own listener already
// applies.
func startMemberAnchorAdvertiser(f *startFlags, caCertPEM []byte, logger *slog.Logger) (*clusterenroll.Advertiser, error) {
	return clusterenroll.StartAnchorAdvertiser(caCertPEM, clusterenroll.AdvertiseOptions{
		NoisePort: f.bootstrapNoisePort,
		NoiseHost: resolveNoiseHost(f),
		AdminPort: f.adminPort,
	}, f.dataDir, logger)
}
