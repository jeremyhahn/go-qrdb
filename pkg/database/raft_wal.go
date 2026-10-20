// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package database

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/logdb/waldb"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch"
	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// WALFormat names how a host's Raft WAL records reach disk. The two formats
// are not interchangeable: records written WithoutEncryption are raw, records
// written WithBarrier go through the barrier (which encrypts them, or passes
// them through while the barrier is in plaintext mode and encrypts from the
// moment encryption is enabled). Reading one as the other is not a migration.
type WALFormat string

const (
	// WALFormatPlaintext is a WAL written WithoutEncryption: seal disabled.
	WALFormatPlaintext WALFormat = "plaintext"

	// WALFormatBarrier is a WAL written WithBarrier under the system barrier,
	// by the default WAL or by the multiplex's shared route -- one directory,
	// one record format, so a host may move between the two across restarts.
	WALFormatBarrier WALFormat = "barrier"
)

// walFormatMarker is the file in a WAL directory that records its WALFormat.
// go-quicraft's waldb carries no format marker of its own, so without this a
// WAL written in one format and opened in the other would surface as decrypt
// or corruption errors on replay rather than as the configuration change it
// is.
const walFormatMarker = ".qrdb-wal-format"

// RaftWALConfig describes the Raft WAL a host needs. Both qrdbd and the SDK
// node build their host's WAL through NewRaftWAL from one of these, so the two
// cannot drift into different at-rest postures.
type RaftWALConfig struct {
	// Dir is the host's WAL directory (HostConfig.WALDir).
	Dir string

	// SystemBarrier is the unsealed system barrier, or nil when seal is
	// disabled.
	SystemBarrier *seal.EpochBarrier

	// UnmarkedFormat is the format an existing WAL directory that carries no
	// format marker was written in by this caller before the marker existed:
	// qrdbd always wrote the format it writes now, while the SDK node wrote
	// WALFormatPlaintext regardless of seal. Empty refuses every unmarked,
	// non-empty WAL as unknown.
	UnmarkedFormat WALFormat
}

// RaftWAL is the at-rest configuration of a host's Raft log: the host options
// to pass to quicraft.NewHost and, on a host with a system barrier, the
// multiplex LogDB the options install.
type RaftWAL struct {
	// Options configure quicraft.NewHost's at-rest encryption, LogDB and
	// metrics collector.
	Options []quicraft.HostOption

	// Multiplex is the host's LogDB when it is the multiplex (shared system
	// barrier WAL plus per-tenant dedicated WALs), nil otherwise. Hand it to
	// the tenant infrastructure so dedicated-WAL shards register in it.
	Multiplex *MultiplexLogDB

	// Metrics is the collector Options hands the host: the process's shared
	// go-quicraft collector, warning on a slow fsync of this WAL. The shared
	// WAL under Multiplex reports its fsyncs to it as well.
	Metrics *RaftMetrics

	// Format is the WAL's format, recorded in its directory.
	Format WALFormat
}

// Close releases the multiplex when the host that was to own it was never
// created. After a successful quicraft.NewHost the host owns the LogDB.
func (w *RaftWAL) Close() error {
	if w == nil || w.Multiplex == nil {
		return nil
	}
	return w.Multiplex.Close()
}

// RaftWALFormatError refuses a WAL directory whose recorded format is not the
// one this host would write: a WAL written WithoutEncryption opened under a
// barrier, or the reverse. Converting it in place is not supported; start the
// node from an empty WAL directory (the replica re-syncs from its peers).
type RaftWALFormatError struct {
	Dir   string
	Found WALFormat
	Want  WALFormat
}

// Error describes the refusal and the remedy.
func (e *RaftWALFormatError) Error() string {
	found := string(e.Found)
	if found == "" {
		found = "unknown (unmarked)"
	}
	return "database: raft WAL in " + e.Dir + " was written as " + found +
		" but this node writes " + string(e.Want) +
		"; it is not converted in place: start from an empty WAL directory"
}

// RaftWALMarkerError reports a WAL format marker that could not be read or
// written.
type RaftWALMarkerError struct {
	Path string
	Err  error
}

// Error describes the failure.
func (e *RaftWALMarkerError) Error() string {
	return "database: raft WAL format marker " + e.Path + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *RaftWALMarkerError) Unwrap() error { return e.Err }

// NewRaftWAL decides the host's Raft WAL from its at-rest posture, refuses a
// WAL directory written in the other format, records the format, and returns
// the host options:
//
//   - No system barrier (seal disabled): WithoutEncryption, and no multiplex.
//   - A system barrier, encrypting or in plaintext mode, on a single node or a
//     cluster alike: the multiplex, with WithLogDB, WithBarrier and
//     WithShardBarriers, so dedicated-WAL shards get their own tenant-barrier
//     WAL. One code path whether or not the barrier encrypts yet: under a
//     plaintext-mode barrier the shared waldb declares AtRestHostRouted, which
//     go-quicraft accepts because its one possibly-plaintext route writes
//     through the host's own barrier, and the records encrypt from the moment
//     EnableEncryption flips that barrier.
func NewRaftWAL(cfg RaftWALConfig) (*RaftWAL, error) {
	want := WALFormatPlaintext
	if cfg.SystemBarrier != nil {
		want = WALFormatBarrier
	}
	if err := claimWALFormat(cfg.Dir, want, cfg.UnmarkedFormat); err != nil {
		return nil, err
	}

	collector := NewRaftMetrics(syncwatch.WithAttrs(slog.String("wal_dir", cfg.Dir)))
	withMetrics := quicraft.WithMetrics(collector)
	if cfg.SystemBarrier == nil {
		return &RaftWAL{Format: want, Metrics: collector, Options: []quicraft.HostOption{quicraft.WithoutEncryption(), withMetrics}}, nil
	}

	// quicraft threads the collector only into a WAL it opens itself, so the
	// shared WAL under the multiplex is handed it here.
	shared, err := waldb.Open(cfg.Dir, waldb.WithBarrier(cfg.SystemBarrier), waldb.WithMetrics(collector))
	if err != nil {
		return nil, err
	}
	mux, err := NewMultiplexLogDB(shared, cfg.SystemBarrier)
	if err != nil {
		_ = shared.Close()
		return nil, err
	}
	return &RaftWAL{
		Format:    want,
		Multiplex: mux,
		Metrics:   collector,
		Options: []quicraft.HostOption{
			quicraft.WithLogDB(mux),
			quicraft.WithBarrier(cfg.SystemBarrier),
			quicraft.WithShardBarriers(func(shardID uint64) quicraft.Barrier {
				if b := mux.BarrierForShard(shardID); b != nil {
					return b
				}
				return nil
			}),
			withMetrics,
		},
	}, nil
}

// claimWALFormat refuses dir when it holds a WAL in a format other than want,
// and records want in it otherwise. A missing or empty directory is fresh. An
// unmarked directory with content is judged by unmarked, the format its
// caller wrote before the marker existed.
func claimWALFormat(dir string, want, unmarked WALFormat) error {
	marker := filepath.Join(dir, walFormatMarker)
	data, err := os.ReadFile(marker) // #nosec G304 -- marker is the host's own WAL directory joined with a constant name
	switch {
	case err == nil:
		if found := WALFormat(strings.TrimSpace(string(data))); found != want {
			return &RaftWALFormatError{Dir: dir, Found: found, Want: want}
		}
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return &RaftWALMarkerError{Path: marker, Err: err}
	}

	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return &RaftWALMarkerError{Path: dir, Err: err}
	}
	if walHasContent(entries) && unmarked != want {
		return &RaftWALFormatError{Dir: dir, Found: unmarked, Want: want}
	}
	return writeWALFormat(dir, marker, want)
}

// walHasContent reports whether a WAL directory holds anything but the format
// marker and the temporary file a crashed marker write can leave behind.
func walHasContent(entries []os.DirEntry) bool {
	for _, e := range entries {
		if name := e.Name(); name != walFormatMarker && name != walFormatMarker+".tmp" {
			return true
		}
	}
	return false
}

// writeWALFormat records format durably: written to a temporary file, synced,
// and renamed over the marker by fsync.Rename, which syncs the directory.
func writeWALFormat(dir, marker string, format WALFormat) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return &RaftWALMarkerError{Path: dir, Err: err}
	}
	tmp := marker + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- tmp is the host's own WAL directory joined with a constant name
	if err != nil {
		return &RaftWALMarkerError{Path: tmp, Err: err}
	}
	if _, err := f.WriteString(string(format) + "\n"); err != nil {
		_ = f.Close()
		return &RaftWALMarkerError{Path: tmp, Err: err}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return &RaftWALMarkerError{Path: tmp, Err: err}
	}
	if err := f.Close(); err != nil {
		return &RaftWALMarkerError{Path: tmp, Err: err}
	}
	if err := fsync.Rename(tmp, marker); err != nil {
		return &RaftWALMarkerError{Path: marker, Err: err}
	}
	return nil
}
