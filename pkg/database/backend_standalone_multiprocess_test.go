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

//go:build !integration

package database

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
)

// The tests in this file prove the guarded verbs are atomic ACROSS PROCESSES on
// the file engine (go-qrdb#632): the test binary re-executes itself as several
// child processes that open ONE file database and race. Goroutines in one
// process would only exercise StandaloneBackend.writeMu; separate processes have
// no shared memory, so nothing but the engine's OS file lock can make them
// agree.

const (
	helperModeEnv   = "QRDB_FILE_MULTIPROCESS_MODE"
	helperDirEnv    = "QRDB_FILE_MULTIPROCESS_DIR"
	helperStartEnv  = "QRDB_FILE_MULTIPROCESS_START"
	helperRoundsEnv = "QRDB_FILE_MULTIPROCESS_ROUNDS"

	helperModeCAS         = "cas"
	helperModePutIfAbsent = "putifabsent"

	// winsPrefix marks the one stdout line a child reports its results on.
	winsPrefix = "WINS "

	multiProcessChildren = 4
	multiProcessRounds   = 40
	counterKey           = "counter"

	// multiProcessChildTimeout bounds each child's whole run: a child still
	// running when it expires is killed, so a hung child fails the test
	// instead of outliving it.
	multiProcessChildTimeout = 2 * time.Minute
	// multiProcessWaitDelay bounds how long Wait waits for a killed child's
	// output pipes to close.
	multiProcessWaitDelay = 5 * time.Second
)

// childPath returns the path the parent passed in env, cleaned, and refuses one
// that is not absolute or does not lie under the temp directory: the parent only
// ever hands a child a path it built from t.TempDir().
func childPath(t *testing.T, env string) string {
	t.Helper()
	p := filepath.Clean(os.Getenv(env))
	rel, err := filepath.Rel(filepath.Clean(os.TempDir()), p)
	if !filepath.IsAbs(p) || err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("%s=%q is not an absolute path under the temp directory", env, p)
	}
	return p
}

// TestMultiProcessHelper is the child side. Run as an ordinary test it does
// nothing; the parent tests re-execute the binary with helperModeEnv set, and
// then this is the whole of the child's work.
func TestMultiProcessHelper(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		return
	}
	rounds, err := strconv.Atoi(os.Getenv(helperRoundsEnv))
	if err != nil {
		t.Fatalf("rounds: %v", err)
	}
	dir := childPath(t, helperDirEnv)
	startFile := childPath(t, helperStartEnv)
	store, err := file.New(dir)
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}
	defer func() { _ = store.Close() }()
	backend := NewStandaloneBackend(store)

	// Hold at the line until the parent has started every child, so the
	// children contend for the whole run instead of running one after another.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(startFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("start signal never arrived")
		}
		time.Sleep(time.Millisecond)
	}

	ctx := context.Background()
	var won []string
	switch mode {
	case helperModeCAS:
		for len(won) < rounds {
			cur, err := backend.Get(ctx, []byte(counterKey))
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			n, err := strconv.Atoi(string(cur))
			if err != nil {
				t.Fatalf("counter %q: %v", cur, err)
			}
			swapped, err := backend.CompareAndSwap(ctx, []byte(counterKey), cur, []byte(strconv.Itoa(n+1)))
			if err != nil {
				t.Fatalf("CompareAndSwap: %v", err)
			}
			if swapped {
				won = append(won, strconv.Itoa(n+1))
			}
		}
	case helperModePutIfAbsent:
		for i := range rounds {
			key := "round-" + strconv.Itoa(i)
			stored, err := backend.PutIfAbsent(ctx, []byte(key), []byte(strconv.Itoa(os.Getpid())))
			if err != nil {
				t.Fatalf("PutIfAbsent: %v", err)
			}
			if stored {
				won = append(won, key)
			}
		}
	default:
		t.Fatalf("unknown mode %q", mode)
	}
	// A report that cannot be written fails the parent's parse of this child.
	_, _ = os.Stdout.WriteString(winsPrefix + strings.Join(won, ",") + "\n")
}

// runChildren starts multiProcessChildren copies of this test binary in mode,
// releases them together, and returns each one's reported wins.
func runChildren(t *testing.T, mode, dir string) [][]string {
	t.Helper()
	start := filepath.Join(t.TempDir(), "start")
	ctx, cancel := context.WithTimeout(t.Context(), multiProcessChildTimeout)
	t.Cleanup(cancel)

	type child struct {
		cmd  *exec.Cmd
		out  bytes.Buffer
		err  error
		done chan struct{}
	}
	children := make([]*child, multiProcessChildren)
	for i := range children {
		c := &child{done: make(chan struct{})}
		// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, and the argument is a compile-time literal. No shell is involved.
		c.cmd = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMultiProcessHelper$")
		c.cmd.Env = append(os.Environ(),
			helperModeEnv+"="+mode,
			helperDirEnv+"="+dir,
			helperStartEnv+"="+start,
			helperRoundsEnv+"="+strconv.Itoa(multiProcessRounds),
		)
		c.cmd.Stdout = &c.out
		c.cmd.Stderr = &c.out
		c.cmd.WaitDelay = multiProcessWaitDelay
		if err := c.cmd.Start(); err != nil {
			t.Fatalf("start child %d: %v", i, err)
		}
		go func() {
			c.err = c.cmd.Wait()
			close(c.done)
		}()
		// Kill and reap the child however the test ends, including a
		// t.Fatalf before the rest have started.
		t.Cleanup(func() {
			_ = c.cmd.Process.Kill()
			<-c.done
		})
		children[i] = c
	}
	if err := os.WriteFile(start, nil, 0o600); err != nil {
		t.Fatalf("write start signal: %v", err)
	}

	wins := make([][]string, len(children))
	for i, c := range children {
		<-c.done
		if c.err != nil {
			t.Errorf("child %d: %v (context: %v)\n%s", i, c.err, ctx.Err(), c.out.String())
			continue
		}
		reported := false
		for _, line := range strings.Split(c.out.String(), "\n") {
			if rest, ok := strings.CutPrefix(line, winsPrefix); ok {
				if rest != "" {
					wins[i] = strings.Split(rest, ",")
				}
				reported = true
				break
			}
		}
		if !reported {
			t.Errorf("child %d reported no result:\n%s", i, c.out.String())
		}
	}
	return wins
}

// TestStandaloneBackend_CompareAndSwap_AtomicAcrossProcesses races several
// processes incrementing one counter with CompareAndSwap. A lost update -- two
// processes both passing the compare against the same value -- would leave the
// final counter short of the number of increments the processes report winning,
// and would make two processes win the same counter value.
func TestStandaloneBackend_CompareAndSwap_AtomicAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	seed, err := file.New(dir)
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}
	ctx := context.Background()
	if stored, err := NewStandaloneBackend(seed).PutIfAbsent(ctx, []byte(counterKey), []byte("0")); err != nil || !stored {
		t.Fatalf("seed counter: stored=%v err=%v", stored, err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed: %v", err)
	}

	wins := runChildren(t, helperModeCAS, dir)

	seen := make(map[string]int)
	total := 0
	for i, w := range wins {
		if len(w) != multiProcessRounds {
			t.Errorf("child %d won %d swaps, want %d", i, len(w), multiProcessRounds)
		}
		for _, v := range w {
			seen[v]++
			total++
		}
	}
	for v, n := range seen {
		if n != 1 {
			t.Errorf("counter value %s was won by %d processes, want exactly 1", v, n)
		}
	}

	check, err := file.New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = check.Close() }()
	final, err := NewStandaloneBackend(check).Get(ctx, []byte(counterKey))
	if err != nil {
		t.Fatalf("final Get: %v", err)
	}
	want := multiProcessChildren * multiProcessRounds
	if got, _ := strconv.Atoi(string(final)); got != want || total != want {
		t.Fatalf("final counter = %s, wins = %d, want both %d (no lost updates)", final, total, want)
	}
}

// TestStandaloneBackend_PutIfAbsent_ExactlyOneWinnerAcrossProcesses races the
// same set of keys from several processes: for every key exactly one process may
// be told it created it.
func TestStandaloneBackend_PutIfAbsent_ExactlyOneWinnerAcrossProcesses(t *testing.T) {
	dir := t.TempDir()

	wins := runChildren(t, helperModePutIfAbsent, dir)

	winners := make(map[string]int)
	for _, w := range wins {
		for _, key := range w {
			winners[key]++
		}
	}
	if len(winners) != multiProcessRounds {
		t.Errorf("%d keys were created, want %d", len(winners), multiProcessRounds)
	}
	for key, n := range winners {
		if n != 1 {
			t.Errorf("%s was created by %d processes, want exactly 1", key, n)
		}
	}
}
