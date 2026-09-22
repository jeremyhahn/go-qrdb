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
		adv, advErr := clusterenroll.ResumeAdvertiser(flagsToEnrollOptions(f), &clustertls.Material{
			CACertPEM: bundle.CACertPEM,
			CAKeyPEM:  bundle.CAKeyPEM,
		}, logger)
		if advErr != nil {
			return nil, nil, advErr
		}
		return bundle, adv, nil
	}

	// Paths 3–5: delegate to clusterenroll.
	opts := flagsToEnrollOptions(f)
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
// clusterenroll.Options value for paths 3–5.
func flagsToEnrollOptions(f *startFlags) *clusterenroll.Options {
	return &clusterenroll.Options{
		RaftAddress: f.raftAddress,
		TrustDomain: f.clusterTrustDomain,
		DataDir:     f.dataDir,
		SingleNode:  isSingleNode(f),
		Bootstrap: clusterenroll.BootstrapOptions{
			NoiseKey:        f.bootstrapNoiseKey,
			NoiseAddr:       f.bootstrapNoiseAddr,
			SPKIPin:         f.bootstrapSPKIPin,
			ServerAddr:      f.bootstrapAddr,
			DANEDNS:         f.bootstrapDANEDNS,
			DANEDoT:         f.bootstrapDANEDoT,
			DirectURL:       f.bootstrapDirectURL,
			DiscoveryMethod: f.discoveryMethod,
			Peers:           f.peers,
			DNSDomain:       f.dnsDomain,
			AdminPort:       f.adminPort,
		},
		Advertise: clusterenroll.AdvertiseOptions{
			NoisePort: f.bootstrapNoisePort,
			NoiseHost: resolveNoiseHost(f),
			AdminPort: f.adminPort,
		},
	}
}

// hasBootstrapFlags returns true if any bootstrap-related flags are set
// on the start command, indicating this node intends to join an existing
// cluster by fetching the CA bundle from a bootstrap server.
func hasBootstrapFlags(f *startFlags) bool {
	return clusterenroll.BootstrapOptions{
		NoiseKey:  f.bootstrapNoiseKey,
		NoiseAddr: f.bootstrapNoiseAddr,
		SPKIPin:   f.bootstrapSPKIPin,
		DANEDNS:   f.bootstrapDANEDNS,
		DirectURL: f.bootstrapDirectURL,
	}.HasFlags()
}

// bootstrapTLS fetches the cluster CA bundle from an existing node using
// go-truststrap, generates a node leaf certificate from the CA material,
// and persists everything to the data directory. It is a thin wrapper
// around clusterenroll.Join that converts the raw clustertls.Material
// into the qrdbd-specific TLSBundle type.
func bootstrapTLS(f *startFlags, logger *slog.Logger) (*TLSBundle, error) {
	opts := flagsToEnrollOptions(f)
	mat, err := clusterenroll.Join(context.Background(), opts, logger)
	if err != nil {
		return nil, err
	}
	return tlsBundleFromMaterial(mat)
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
