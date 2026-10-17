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

package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/logdb"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch/syncwatchtest"
)

// encryptingEpochBarrier is an initialized barrier under a software strategy:
// it encrypts, unlike newInitializedEpochBarrier's plaintext-mode one.
func encryptingEpochBarrier(t *testing.T) *seal.EpochBarrier {
	t.Helper()
	b, err := seal.NewEpochBarrier(seal.DefaultEpochBarrierConfig(1))
	if err != nil {
		t.Fatalf("new epoch barrier: %v", err)
	}
	strategy, err := seal.NewSoftwareStrategy(context.Background(), []byte("raft-wal-test"))
	if err != nil {
		t.Fatalf("software strategy: %v", err)
	}
	if err := b.Initialize(context.Background(), strategy, seal.MustCredentials(context.Background(), []byte("raft-wal-test"))); err != nil {
		t.Fatalf("initialize epoch barrier: %v", err)
	}
	if b.IsPlaintextMode() {
		t.Fatal("software-strategy barrier reports plaintext mode; the fixture is wrong")
	}
	return b
}

func readMarker(t *testing.T, dir string) WALFormat {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, walFormatMarker))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	return WALFormat(strings.TrimSpace(string(data)))
}

// Every posture picks the option set go-quicraft 0.15 accepts for it, and
// records the WAL's format.
func TestNewRaftWAL_ChoosesTheAcceptedPosture(t *testing.T) {
	for _, c := range []struct {
		name      string
		barrier   func(*testing.T) *seal.EpochBarrier
		single    bool
		format    WALFormat
		options   int
		multiplex bool
	}{
		{"seal disabled", func(*testing.T) *seal.EpochBarrier { return nil }, false, WALFormatPlaintext, 2, false},
		{"single node, encrypting", encryptingEpochBarrier, true, WALFormatBarrier, 2, false},
		{"cluster, plaintext-mode barrier", newInitializedEpochBarrier, false, WALFormatBarrier, 2, false},
		{"cluster, encrypting", encryptingEpochBarrier, false, WALFormatBarrier, 4, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			wal, err := NewRaftWAL(RaftWALConfig{Dir: dir, SystemBarrier: c.barrier(t), SingleNode: c.single, UnmarkedFormat: c.format})
			if err != nil {
				t.Fatalf("NewRaftWAL: %v", err)
			}
			defer func() { _ = wal.Close() }()
			if wal.Format != c.format || len(wal.Options) != c.options || (wal.Multiplex != nil) != c.multiplex {
				t.Fatalf("got format %s, %d options, multiplex %v; want %s, %d, %v",
					wal.Format, len(wal.Options), wal.Multiplex != nil, c.format, c.options, c.multiplex)
			}
			if wal.Metrics == nil {
				t.Fatal("no metrics collector: the host would report to nothing /metrics serves")
			}
			if got := readMarker(t, dir); got != c.format {
				t.Fatalf("marker = %q, want %q", got, c.format)
			}
		})
	}
}

// quicraft hands its collector only to a WAL it opens itself; the shared WAL
// under the multiplex is opened here, so its fsyncs reach /metrics only if
// NewRaftWAL passes the collector to waldb.Open.
func TestNewRaftWAL_MultiplexSharedWALReportsFsyncs(t *testing.T) {
	const fsyncFamily = "quicraft_logdb_fsync_latency_seconds"
	wal, err := NewRaftWAL(RaftWALConfig{Dir: t.TempDir(), SystemBarrier: encryptingEpochBarrier(t), UnmarkedFormat: WALFormatBarrier})
	if err != nil {
		t.Fatalf("NewRaftWAL: %v", err)
	}
	defer func() { _ = wal.Close() }()
	if wal.Multiplex == nil {
		t.Fatal("an encrypting cluster WAL did not build the multiplex")
	}
	before, _ := syncwatchtest.HistogramSample(t, SharedRaftMetrics().Registry, fsyncFamily, "", "")

	if err := wal.Multiplex.SaveBootstrap(1, 1, logdb.Bootstrap{Addresses: map[uint64]string{1: "127.0.0.1:1"}, Type: 7}); err != nil {
		t.Fatalf("SaveBootstrap: %v", err)
	}
	if err := wal.Multiplex.SaveState([]logdb.Update{{
		ShardID: 1, ReplicaID: 1,
		State:   logdb.State{Term: 1, Vote: 1, Commit: 1},
		Entries: []logdb.Entry{{Index: 1, Term: 1, Cmd: []byte("fsync-me")}},
	}}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	after, _ := syncwatchtest.HistogramSample(t, SharedRaftMetrics().Registry, fsyncFamily, "", "")
	if after <= before {
		t.Fatalf("%s sample count %d -> %d: the shared WAL's fsync was not observed", fsyncFamily, before, after)
	}
}

// A WAL written in the other format is refused, never converted: an unmarked
// directory with content judged by its writer's earlier format, and a marked
// one whose marker disagrees.
func TestNewRaftWAL_RefusesTheOtherFormat(t *testing.T) {
	unmarked := t.TempDir()
	if err := os.MkdirAll(filepath.Join(unmarked, "shard-0"), 0o700); err != nil {
		t.Fatal(err)
	}
	marked := t.TempDir()
	if err := os.WriteFile(filepath.Join(marked, walFormatMarker), []byte("plaintext\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"unmarked plaintext-era WAL": unmarked, "marked plaintext WAL": marked} {
		_, err := NewRaftWAL(RaftWALConfig{Dir: dir, SystemBarrier: encryptingEpochBarrier(t), UnmarkedFormat: WALFormatPlaintext})
		var formatErr *RaftWALFormatError
		if !errors.As(err, &formatErr) || formatErr.Found != WALFormatPlaintext || formatErr.Want != WALFormatBarrier {
			t.Errorf("%s: err = %v, want a plaintext->barrier RaftWALFormatError", name, err)
		}
	}
}

// An unmarked WAL in the format its writer always used is adopted and marked
// (qrdbd's own data dirs), and a leftover temporary marker from a crashed
// write does not count as WAL content.
func TestNewRaftWAL_AdoptsItsOwnUnmarkedFormat(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "shard-0"), 0o700); err != nil {
		t.Fatal(err)
	}
	wal, err := NewRaftWAL(RaftWALConfig{Dir: dir, SystemBarrier: encryptingEpochBarrier(t), SingleNode: true, UnmarkedFormat: WALFormatBarrier})
	if err != nil {
		t.Fatalf("NewRaftWAL: %v", err)
	}
	_ = wal.Close()
	if got := readMarker(t, dir); got != WALFormatBarrier {
		t.Fatalf("marker = %q, want barrier", got)
	}

	crashed := t.TempDir()
	if err := os.WriteFile(filepath.Join(crashed, walFormatMarker+".tmp"), []byte("barr"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRaftWAL(RaftWALConfig{Dir: crashed, SystemBarrier: encryptingEpochBarrier(t), SingleNode: true}); err != nil {
		t.Fatalf("a leftover temporary marker was taken for WAL content: %v", err)
	}
}
