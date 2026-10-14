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

package file

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

const (
	casChildEnv = "QRDB_FILE_CAS_CHILD"
	casGoFile   = "cas-go"
)

// TestCASChildHelper is the child side of TestCompareAndSwap_AcrossProcesses.
// It opens the store in its working directory, waits for the go file so every
// contender starts together, swaps "start" for its own id and prints "won" or
// "lost".
func TestCASChildHelper(t *testing.T) {
	id := os.Getenv(casChildEnv)
	if id == "" {
		return // not a child: nothing to do outside the parent's re-exec
	}
	e, err := New(".")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = e.Close() }()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(casGoFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the go file")
		}
		time.Sleep(time.Millisecond)
	}
	swapped, err := e.CompareAndSwap(context.Background(), []byte("k"), []byte("start"), []byte("winner-"+id))
	if err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	if swapped {
		os.Stdout.WriteString("won\n")
		return
	}
	os.Stdout.WriteString("lost\n")
}

// Two processes racing CompareAndSwap on one key: exactly one wins, and the
// stored value belongs to the winner.
func TestCompareAndSwap_AcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	seed, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := seed.Set([]byte("k"), []byte("start")); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	const procs = 4
	outs := make([]bytes.Buffer, procs)
	errs := make([]error, procs)
	var wg sync.WaitGroup
	for i := 0; i < procs; i++ {
		// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, and the argument is a compile-time literal. No shell is involved.
		cmd := exec.Command(os.Args[0], "-test.run=^TestCASChildHelper$")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), casChildEnv+"="+strconv.Itoa(i))
		cmd.Stdout = &outs[i]
		cmd.Stderr = &outs[i]
		if err := cmd.Start(); err != nil {
			t.Fatalf("start child %d: %v", i, err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = cmd.Wait()
		}()
	}
	// Let every child open the store and reach the barrier, then release them.
	time.Sleep(500 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, casGoFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	winner := -1
	for i := range outs {
		if errs[i] != nil {
			t.Fatalf("child %d: %v\n%s", i, errs[i], outs[i].String())
		}
		switch {
		case bytes.Contains(outs[i].Bytes(), []byte("won\n")):
			if winner != -1 {
				t.Fatalf("children %d and %d both won", winner, i)
			}
			winner = i
		case bytes.Contains(outs[i].Bytes(), []byte("lost\n")):
		default:
			t.Fatalf("child %d reported nothing:\n%s", i, outs[i].String())
		}
	}
	if winner == -1 {
		t.Fatal("no child won")
	}

	e, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	got, err := e.Get([]byte("k"))
	if err != nil || string(got) != "winner-"+strconv.Itoa(winner) {
		t.Fatalf("stored value = %q, %v; want winner-%d", got, err, winner)
	}
}
