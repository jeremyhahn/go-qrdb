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
	"os"
	"path/filepath"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
)

// confirmClusterCAPersisted triggers the cluster CA (Key A) provider's
// lazy adopt-or-load (a Certificate call is enough; see
// cakey.SoftwareProvider's "material" accessor) and, once it succeeds,
// deletes the local ca-key.pem cache -- the file is a bootstrap-only
// artifact once the replicated store durably holds the same key.
//
// hadLocalKey records whether ca-key.pem existed on disk THIS boot (bundle
// loaded or just generated it); when false there is nothing to delete; a
// node past its first successful boot already has none.
//
// A failure here is fatal: proceeding with the data plane up but no
// confirmed, durable cluster CA would let this node issue tenant logins
// while its own cluster identity was never made durable, and silently
// leaves ca-key.pem (if present) as the only copy of a key this process
// might not hold in memory on its next restart.
func confirmClusterCAPersisted(ctx context.Context, provider cakey.Provider, dataDir string, hadLocalKey bool) error {
	if _, err := provider.Certificate(ctx); err != nil {
		return &ClusterCAPersistError{Err: err}
	}
	if !hadLocalKey {
		return nil
	}

	keyPath := filepath.Join(dataDir, "ca-key.pem")
	if err := os.Remove(keyPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		// Not fatal: the key is durably persisted either way, and the
		// stale local copy is a cleanup failure, not a correctness one.
		// Logged at ERROR because a key file lingering on disk is exactly
		// the state this change set out to eliminate.
		slog.Error("cluster CA key persisted to the replicated store, but the local cache could not be removed",
			slog.String("path", keyPath), slog.String("error", err.Error()))
		return nil
	}
	slog.Info("cluster CA key persisted to the replicated store; local file cache removed", slog.String("path", keyPath))
	return nil
}

// ClusterCAPersistError is returned when the cluster CA (Key A) provider's
// first use -- adopting in-memory bootstrap material into the replicated
// store, or loading what a prior boot already persisted -- fails.
type ClusterCAPersistError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ClusterCAPersistError) Error() string {
	return "qrdbd: cluster CA persistence check failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ClusterCAPersistError) Unwrap() error {
	return e.Err
}
