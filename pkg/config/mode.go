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

package config

import (
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
)

// standaloneReason is the remedy text shared by every raft-only setting
// refused under mode "standalone".
const standaloneReason = "requires mode: raft (a standalone node has no cluster to join)"

// IsStandalone reports whether the configuration selects the raft-free
// standalone run mode. An empty Mode means "raft" for backward
// compatibility.
func (c *Config) IsStandalone() bool {
	return c.Mode == capabilities.ModeStandalone
}

// validateMode enforces the three-gate feature model at the config gate:
// the mode enum itself and the cluster settings that are structurally
// impossible without raft. MVCC, watch and lease are served in both modes
// (go-qrdb#582), so their enabled toggles are valid under either.
func (c *Config) validateMode() error {
	switch c.Mode {
	case "", capabilities.ModeRaft:
	case capabilities.ModeStandalone:
		if c.Raft.Join {
			return &ValidationError{Field: "raft.join", Reason: standaloneReason}
		}
		if peerCount(c.Raft.Peers) > 1 {
			return &ValidationError{Field: "raft.peers", Reason: standaloneReason}
		}
	default:
		return &ValidationError{Field: "mode", Reason: `must be "standalone" or "raft"`}
	}
	return nil
}

// ValidateCapabilities checks the configuration against the build-tag
// registrations of THIS binary and refuses features that are enabled in
// config but not compiled in, naming the missing tag. It is deliberately
// separate from Validate — which judges only the configuration itself —
// and is called by composition roots (the daemon, the SDK node) at boot.
func (c *Config) ValidateCapabilities() error {
	if c.Protocols.MySQL && !capabilities.Has("mysql") {
		return &ValidationError{Field: "protocols.mysql", Reason: "not compiled in: requires build tag qrdb_mysql"}
	}
	if c.Protocols.PostgreSQL && !capabilities.Has("pgsql") {
		return &ValidationError{Field: "protocols.postgresql", Reason: "not compiled in: requires build tag qrdb_pgsql"}
	}
	return nil
}

// smStorageFactoryTypes are the storage-generic state machine families
// that compose the registry storage factory and therefore honor
// raft.sm_storage. The pebble-native SMs ("pebble", "mvcc", ...) carry
// their own dedicated Pebble store.
var smStorageFactoryTypes = map[string]struct{}{
	"disk": {},
	"fast": {},
}

// validateSMStorage enforces the raft.sm_storage contract: a valid
// backend name, objstore refused as the hot apply path, and a non-pebble
// selection only combined with a storage-generic state machine.
func (c *Config) validateSMStorage() error {
	switch c.Raft.SMStorage {
	case "", "pebble", "file", "memory":
	case "objstore":
		return &ValidationError{
			Field:  "raft.sm_storage",
			Reason: "objstore is a snapshot/backup tier and cannot back the state machine hot apply path; use pebble, file, or memory",
		}
	default:
		return &ValidationError{
			Field:  "raft.sm_storage",
			Reason: "must be one of: pebble, file, memory",
		}
	}
	if c.Raft.SMStorage == "" || c.Raft.SMStorage == "pebble" {
		return nil
	}
	_, typeOK := smStorageFactoryTypes[c.Raft.SMType]
	_, backendOK := smStorageFactoryTypes[c.Raft.SMBackend]
	if !typeOK && !backendOK {
		return &ValidationError{
			Field:  "raft.sm_storage",
			Reason: "applies to the storage-generic state machines only; set raft.sm_type (or sm_backend) to disk or fast",
		}
	}
	return nil
}

// peerCount counts non-empty entries in a comma-separated raft peer list.
func peerCount(peers string) int {
	n := 0
	for _, p := range strings.Split(peers, ",") {
		if strings.TrimSpace(p) != "" {
			n++
		}
	}
	return n
}

// EffectiveCapabilities computes the runtime capability profile for this
// configuration: the intersection of build-tag registrations
// (capabilities.Has), the run mode, and config toggles. Composition roots
// pass the result to capabilities.Seal exactly once at boot, before
// serving traffic. The config must have been validated first.
func (c *Config) EffectiveCapabilities() capabilities.Capabilities {
	raft := !c.IsStandalone()
	enabled := func(p *bool) bool { return p == nil || *p }

	mode := capabilities.ModeRaft
	if !raft {
		mode = capabilities.ModeStandalone
	}

	return capabilities.Capabilities{
		Mode: mode,

		// The always-compiled core.
		KV:    true,
		Batch: true,
		Index: true,

		// Transactions / compare-and-swap are served in EVERY mode (owner
		// ruling 2026-08-23, go-qrdb#62): consensus is what makes a
		// compare-and-swap linearizable across REPLICAS, and a standalone
		// node is engine-direct and single-process with none. Reporting
		// false here while both the SDK's in-process transport and the
		// daemon's gated registry serve it would make the capability report
		// the lie instead of the gate.
		//
		// Like MVCC and the other engine-shaped rows, this is a MODE-level
		// statement. The file and objstore backends compose the plain KV
		// engine and cannot execute a transaction on any mode; that is the
		// engine boundary (docs/run-modes.md), reported per operation as a
		// typed engine.UnsupportedOperationError, not per node here.
		Transaction: true,

		// The coordination families are served in EVERY mode too
		// (go-qrdb#582): a standalone node composes them over the same
		// engine, lessor and watch hub as raft mode's MVCC state machine,
		// applying commands in process instead of through the log. Config
		// toggles narrow them in either mode. Like Transaction these are
		// MODE-level statements: the file and objstore backends compose the
		// plain KV engine, which keeps no revisions and no leases, and refuse
		// those operations per call with a typed capability error.
		MVCC:  enabled(c.MVCC.Enabled),
		Watch: enabled(c.Watch.Enabled),
		Lease: enabled(c.Lease.Enabled),
		Lock:  true,

		// Cluster-shaped features stay raft-only.
		Cluster:   raft,
		Backup:    raft,
		Rebalance: raft,
		Etcd:      raft && capabilities.Has("etcd"),

		// Build-tag-gated features available in both modes.
		TimeSeries: capabilities.Has("timeSeries"),
		Vector:     capabilities.Has("vector"),
		SQL:        capabilities.Has("sql"),
		MySQL:      capabilities.Has("mysql"),
		PostgreSQL: capabilities.Has("pgsql"),
		FTS:        capabilities.Has("fts"),

		UnencryptedAtRest: c.unencryptedAtRest(raft),
	}
}

// unencryptedLocalBackends are the standalone storage backends that keep
// data without the barrier's encrypting file system. Pebble and file write
// their files through it; objstore keeps its objects in a bucket, which has
// no file system to encrypt. The memory backend keeps nothing at rest.
var unencryptedLocalBackends = map[string]struct{}{
	"objstore": {},
}

// unencryptedAtRest reports where, with seal enabled, the node's data plane
// keeps data unencrypted: a standalone node's objstore backend (the backend
// of the first configured database, pebble when none is set). A raft node
// reports nothing: every state machine storage it will start under the
// barrier (pebble, file) is encrypted at rest, or persists nothing (memory).
// It returns nil when seal is disabled.
func (c *Config) unencryptedAtRest(raft bool) []string {
	if !c.Seal.Enabled || raft {
		return nil
	}
	backend := "pebble"
	if len(c.Databases) > 0 && c.Databases[0].Backend != "" {
		backend = c.Databases[0].Backend
	}
	if _, ok := unencryptedLocalBackends[backend]; ok {
		return []string{`standalone backend "` + backend + `" stores its data without the seal barrier's at-rest encryption; use backend "pebble" or "file"`}
	}
	return nil
}
