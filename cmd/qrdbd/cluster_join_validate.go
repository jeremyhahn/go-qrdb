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

import "strings"

// validateClusterJoinFlags re-states, for the pure-flags start path, the two
// member-side CSR join invariants pkg/config.Config.Validate already
// enforces for a `--config` YAML load: resolveStartFlags only calls
// cfg.ValidateCapabilities on that path, never the full Validate, so a node
// started with no --config at all (CLI flags only, the common case) never
// reaches those checks. This is the narrow fix: it re-checks exactly the
// two invariants named for this surface, not a general flags/YAML parity
// pass over every field Validate covers.
func validateClusterJoinFlags(f *startFlags) error {
	if f.clusterJoinEnabled && !f.sealConfig.Enabled {
		return &ClusterJoinRequiresSealError{}
	}
	if f.bootstrapJoinHTTPURL != "" && !strings.HasPrefix(f.bootstrapJoinHTTPURL, "https://") {
		return &JoinHTTPURLRequiresHTTPSError{URL: f.bootstrapJoinHTTPURL}
	}
	return nil
}

// ClusterJoinRequiresSealError is returned when --cluster-join-enabled is
// set with no seal barrier configured: member-side CSR signing has no
// meaning without a barrier to gate Key A behind. Mirrors
// pkg/config.Config.Validate's "cluster_join.enabled" check for the
// pure-flags start path, which never evaluates that function.
type ClusterJoinRequiresSealError struct{}

// Error returns a human-readable description.
func (e *ClusterJoinRequiresSealError) Error() string {
	return "qrdbd: --cluster-join-enabled requires a seal barrier " +
		"(--seal-enabled or seal.enabled in YAML); member-side CSR signing " +
		"has no meaning without a barrier to gate the cluster CA key behind"
}

// JoinHTTPURLRequiresHTTPSError is returned when --bootstrap-join-url is
// set to a non-https:// URL. The SPKI/Direct join transport trusts the
// connection through TLS (SPKI pinning or the system trust store); a plain
// http:// URL would carry the join token and the issued leaf in the clear.
type JoinHTTPURLRequiresHTTPSError struct {
	URL string
}

// Error returns a human-readable description.
func (e *JoinHTTPURLRequiresHTTPSError) Error() string {
	return "qrdbd: --bootstrap-join-url must use https://, got " + e.URL
}
