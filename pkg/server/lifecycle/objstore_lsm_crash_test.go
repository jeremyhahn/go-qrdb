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

package lifecycle_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// The child half of TestObjstoreLSM_KillMidCompaction reads its directories
// and the barrier snapshot from these variables.
const (
	crashChildEnv    = "QRDB_OBJSTORE_LSM_CRASH_CHILD"
	crashDataDirEnv  = "QRDB_OBJSTORE_LSM_CRASH_DATADIR"
	crashBucketEnv   = "QRDB_OBJSTORE_LSM_CRASH_BUCKET"
	crashBarrierEnv  = "QRDB_OBJSTORE_LSM_CRASH_BARRIER"
	crashPassphrase  = "objstore-lsm-crash-passphrase"
	crashRootKeyPath = "objstore-lsm-crash-root-key"
	crashBatches     = 4
	crashBatchKeys   = 64
	crashMaxRounds   = 32
)

func crashKey(round, batch, i int) string {
	return fmt.Sprintf("r%02d/b%02d/k%04d", round, batch, i)
}

func crashValue(key string) []byte {
	return []byte(strings.Repeat(key+"|", 32))
}

func crashStorageConfig(bucket string) qrdbconfig.StorageConfig {
	return qrdbconfig.StorageConfig{Objstore: qrdbconfig.ObjstoreConfig{Provider: "local", Settings: map[string]string{"path": bucket}}}
}

// newCrashBarrier builds a passphrase barrier over backend and unseals it,
// initializing it first when init is set.
func newCrashBarrier(backend *seal.MemoryBackend, init bool) (*seal.StorageBarrier, error) {
	ctx := context.Background()
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte(crashPassphrase))
	if err != nil {
		return nil, err
	}
	barrier, err := seal.NewStorageBarrier(slog.New(slog.NewTextHandler(io.Discard, nil)),
		backend, seal.BarrierConfig{RootKeyPath: crashRootKeyPath, DeploymentID: 1}, strategy)
	if err != nil {
		return nil, err
	}
	creds := seal.MustCredentials(ctx, []byte(crashPassphrase))
	if init {
		if err := barrier.Initialize(ctx, creds); err != nil {
			return nil, err
		}
	}
	if barrier.IsSealed() {
		if err := barrier.Unseal(ctx, creds); err != nil {
			return nil, err
		}
	}
	return barrier, nil
}

// restoreCrashBarrier rebuilds the barrier's backend from the JSON snapshot
// at path and unseals a fresh barrier over it with the passphrase.
func restoreCrashBarrier(path string) (*seal.StorageBarrier, error) {
	// #nosec G703 -- path names the barrier snapshot this test wrote under t.TempDir(), or the same path the parent passed to its re-exec'd child through crashBarrierEnv. Nothing from outside the test process chooses it.
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	snapshot := map[string][]byte{}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	backend := seal.NewMemoryBackend()
	for k, v := range snapshot {
		if err := backend.Put(context.Background(), k, v); err != nil {
			return nil, err
		}
	}
	return newCrashBarrier(backend, false)
}

// TestObjstoreLSM_KillMidCompaction kills a process running the objstore
// engine (lifecycle.NewLocalStorage with database.BackendObjstore) while a
// manual compaction over its bucket SSTables is in progress, then reopens the
// same data directory and bucket with a cold cache (the local object cache
// directory removed, so reads fetch SSTables from the bucket): every
// acknowledged key reads back, a
// further write survives a Close and reopen, and the bucket's LSM format
// marker is still version 3. It runs with the barrier off and on.
func TestObjstoreLSM_KillMidCompaction(t *testing.T) {
	if os.Getenv(crashChildEnv) == "1" {
		runObjstoreLSMCrashChild()
		return
	}
	for _, encrypted := range []bool{false, true} {
		name := "barrier off"
		if encrypted {
			name = "barrier on"
		}
		t.Run(name, func(t *testing.T) {
			killObjstoreLSMMidCompaction(t, encrypted)
		})
	}
}

func killObjstoreLSMMidCompaction(t *testing.T, encrypted bool) {
	dataDir := t.TempDir()
	bucket := filepath.Join(t.TempDir(), "bucket")
	env := append(os.Environ(), crashChildEnv+"=1", crashDataDirEnv+"="+dataDir, crashBucketEnv+"="+bucket)

	enc := atrest.New(nil)
	if encrypted {
		backend := seal.NewMemoryBackend()
		if _, err := newCrashBarrier(backend, true); err != nil {
			t.Fatalf("barrier: %v", err)
		}
		snapshot := map[string][]byte{}
		if err := backend.Scan(context.Background(), "", func(k string, v []byte) error {
			snapshot[k] = v
			return nil
		}); err != nil {
			t.Fatalf("snapshot the barrier backend: %v", err)
		}
		raw, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatalf("marshal the barrier snapshot: %v", err)
		}
		path := filepath.Join(t.TempDir(), "barrier.json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatalf("write the barrier snapshot: %v", err)
		}
		env = append(env, crashBarrierEnv+"="+path)
		// The parent reopens as a restarted node would: a fresh barrier
		// unsealed from the same persisted barrier state as the child's.
		barrier, err := restoreCrashBarrier(path)
		if err != nil {
			t.Fatalf("restore the barrier: %v", err)
		}
		enc = atrest.New(barrier)
	}

	// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, and every other argument is a compile-time literal. No shell is involved and nothing from outside the process reaches this argv.
	cmd := exec.Command(os.Args[0], "-test.run=^TestObjstoreLSM_KillMidCompaction$")
	cmd.Env = env
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the child: %v", err)
	}
	var acked []string
	compacting := false
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		switch {
		case len(fields) == 3 && fields[0] == "ack":
			round, rerr := strconv.Atoi(fields[1])
			batch, berr := strconv.Atoi(fields[2])
			if rerr != nil || berr != nil {
				t.Fatalf("malformed ack line %q", scanner.Text())
			}
			for i := 0; i < crashBatchKeys; i++ {
				acked = append(acked, crashKey(round, batch, i))
			}
		case len(fields) == 1 && fields[0] == "compacting":
			compacting = true
		}
	}
	waitErr := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("child exited with %v, want a SIGKILL", waitErr)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child exited with %v, want a SIGKILL mid-compaction", waitErr)
	}
	if !compacting || len(acked) == 0 {
		t.Fatalf("child was killed before a compaction began (compacting %v, %d acknowledged keys)", compacting, len(acked))
	}

	// Reopen with a cold cache: drop the local object cache the child filled
	// (objstore.CachedRemote under <dataDir>/objstore-lsm-cache) and keep the
	// Pebble dir (WAL, MANIFEST) and the bucket, so every acknowledged key
	// below reads its SSTables back from the bucket.
	cacheDir := filepath.Join(dataDir, "objstore-lsm-cache")
	if entries, err := os.ReadDir(cacheDir); err != nil || len(entries) == 0 {
		t.Fatalf("the child left no local object cache to remove (%d entries, %v)", len(entries), err)
	}
	if err := os.RemoveAll(cacheDir); err != nil {
		t.Fatalf("remove the local object cache: %v", err)
	}
	if _, err := os.Stat(cacheDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("local object cache still present after removal: %v", err)
	}

	cfg := crashStorageConfig(bucket)
	reopened, _, err := lifecycle.NewLocalStorage(database.BackendObjstore, dataDir, enc, cfg)
	if err != nil {
		t.Fatalf("reopen after the kill: %v", err)
	}
	for _, k := range acked {
		got, err := reopened.Get([]byte(k))
		if err != nil {
			t.Fatalf("Get(%q) after the kill: %v", k, err)
		}
		if string(got) != string(crashValue(k)) {
			t.Fatalf("Get(%q) after the kill returned a different value", k)
		}
	}
	if err := reopened.Set([]byte("after-crash"), []byte("written")); err != nil {
		t.Fatalf("write after the kill: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close after the kill: %v", err)
	}

	again, _, err := lifecycle.NewLocalStorage(database.BackendObjstore, dataDir, enc, cfg)
	if err != nil {
		t.Fatalf("second reopen: %v", err)
	}
	t.Cleanup(func() { _ = again.Close() })
	got, err := again.Get([]byte("after-crash"))
	if err != nil || string(got) != "written" {
		t.Fatalf("Get(after-crash) after the second reopen = %q, %v", got, err)
	}
	marker, err := os.ReadFile(filepath.Join(bucket, "qrdb-objstore-format"))
	if err != nil {
		t.Fatalf("read the format marker: %v", err)
	}
	if string(marker) != "3" {
		t.Errorf("format marker = %q, want %q", marker, "3")
	}
}

// runObjstoreLSMCrashChild writes and flushes batches so several SSTables
// land in the bucket, acknowledges each flushed batch on stdout, then starts a
// full-range compaction and SIGKILLs its own process once the compaction is
// in progress. A compaction that finishes before it is observed in progress
// is followed by another round, so the kill always lands mid-compaction.
func runObjstoreLSMCrashChild() {
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "crash child: "+format+"\n", args...)
		os.Exit(2)
	}
	enc := atrest.New(nil)
	if path := os.Getenv(crashBarrierEnv); path != "" {
		barrier, err := restoreCrashBarrier(path)
		if err != nil {
			fail("restore the barrier: %v", err)
		}
		enc = atrest.New(barrier)
	}
	stg, pdb, err := lifecycle.NewLocalStorage(database.BackendObjstore, os.Getenv(crashDataDirEnv), enc,
		crashStorageConfig(os.Getenv(crashBucketEnv)))
	if err != nil {
		fail("open: %v", err)
	}
	for round := 0; round < crashMaxRounds; round++ {
		for batch := 0; batch < crashBatches; batch++ {
			for i := 0; i < crashBatchKeys; i++ {
				k := crashKey(round, batch, i)
				if err := stg.Set([]byte(k), crashValue(k)); err != nil {
					fail("Set(%q): %v", k, err)
				}
			}
			if err := pdb.Flush(); err != nil {
				fail("Flush: %v", err)
			}
			_, _ = fmt.Fprintf(os.Stdout, "ack %d %d\n", round, batch)
		}
		done := make(chan error, 1)
		go func() { done <- pdb.Compact(context.Background(), []byte{0x00}, []byte{0xff}, false) }()
		for {
			if pdb.Metrics().Compact.NumInProgress > 0 {
				_, _ = fmt.Fprintln(os.Stdout, "compacting")
				_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
				select {} // the signal ends the process
			}
			select {
			case err := <-done:
				if err != nil {
					fail("Compact: %v", err)
				}
			default:
				runtime.Gosched()
				continue
			}
			break
		}
	}
	fail("no compaction was observed in progress in %d rounds", crashMaxRounds)
}
