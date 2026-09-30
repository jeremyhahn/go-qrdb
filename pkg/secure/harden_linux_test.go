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

//go:build linux

package secure

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The real mechanism is proven against real processes, never against this
// test binary: a non-dumpable process cannot be un-made dumpable by anything
// the test could later observe, so the change is confined to a child that
// exits when the test ends.
//
// Three processes take part, and the shape is forced by the kernel:
//
//	test ──> READER ──> TARGET
//
// TARGET calls HardenProcess (or honours the opt-out), then publishes its
// PR_GET_DUMPABLE value and the address of a random marker it holds.
//
// READER opens /proc/<target>/mem and reads the marker back. It is TARGET's
// PARENT because Yama's ptrace_scope=1, the common distribution default,
// admits a /proc/<pid>/mem open only from an ancestor — a same-UID sibling
// would be refused whether or not the target hardened, and the positive
// control below would fail for a reason that says nothing about
// HardenProcess. It drops CAP_SYS_PTRACE from its effective set first,
// because CI job containers run as root and that capability bypasses the
// dumpable check entirely; without the drop the negative case would pass on
// a developer's workstation and fail on the runner.
//
// The opt-out case is the POSITIVE CONTROL. It proves the reader can read
// the target's memory when the property is absent, so the hardened case's
// refusal is evidence rather than an environment that refuses every read.
const (
	hardenRoleEnv   = "QRDB_SECURE_HARDEN_TEST_ROLE"
	hardenMarkerEnv = "QRDB_SECURE_HARDEN_TEST_MARKER"
	hardenRoleRead  = "reader"
	hardenRoleTgt   = "target"
)

// The helpers' own failures. They surface as a nonzero exit and the parent
// prints the combined output.
var (
	errNoHardenMarker         = errors.New("harden test: target started without a marker")
	errTargetPublishedNothing = errors.New("harden test: target exited before publishing its address")
)

// hardenSubprocessArgs builds a re-exec of this test binary running only the
// named test, forwarding the coverage directory so the child's statements
// count toward the package's figure.
func hardenSubprocessArgs(test string) []string {
	args := []string{"-test.run=^" + test + "$", "-test.timeout=60s"}
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		args = append(args, "-test.gocoverdir="+dir)
	}
	return args
}

// hardenOutcome is what READER reports about one TARGET.
type hardenOutcome struct {
	dumpable int
	openErr  string // "" when /proc/<pid>/mem opened
	matched  bool   // the marker was read back through /proc/<pid>/mem
	warned   bool   // TARGET's stderr named the opt-out variable
}

// runHardenScenario runs READER (which runs TARGET) with the opt-out set to
// optOut and returns what READER observed.
func runHardenScenario(t *testing.T, optOut string) hardenOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// #nosec G204 -- re-exec of this test binary: argv[0] is os.Args[0], the
	// path `go test` invoked, and every other argument is a compile-time
	// literal. No shell is involved.
	cmd := exec.CommandContext(ctx, os.Args[0], hardenSubprocessArgs("TestHardenProcess_ReaderHelper")...)
	cmd.Env = append(os.Environ(),
		hardenRoleEnv+"="+hardenRoleRead,
		AllowProcessInspectionEnv+"="+optOut,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("reader subprocess failed: %v\noutput:\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "OUTCOME ") {
			continue
		}
		var o hardenOutcome
		var openErr string
		if _, serr := fmt.Sscanf(line, "OUTCOME dumpable=%d open=%q matched=%t warned=%t",
			&o.dumpable, &openErr, &o.matched, &o.warned); serr != nil {
			t.Fatalf("unparseable reader outcome %q: %v", line, serr)
		}
		o.openErr = openErr
		return o
	}
	t.Fatalf("reader reported no outcome; output:\n%s", out)
	return hardenOutcome{}
}

// TestHardenProcess_DeniesProcMemToASameUserReader is go-qrdb#554's
// verification: a process that has called HardenProcess reports
// PR_GET_DUMPABLE == 0 and its /proc/<pid>/mem cannot be opened by a reader
// running as the same user; the opt-out leaves it dumpable, readable, and
// logs the warning.
func TestHardenProcess_DeniesProcMemToASameUserReader(t *testing.T) {
	if os.Getenv(hardenRoleEnv) != "" {
		return // a helper invocation reached this test by name; nothing to do
	}

	t.Run("opt-out is inspectable (positive control)", func(t *testing.T) {
		o := runHardenScenario(t, "true")
		if o.dumpable != 1 {
			t.Fatalf("opted-out target PR_GET_DUMPABLE = %d, want 1", o.dumpable)
		}
		if o.openErr != "" {
			t.Fatalf("the reader could not open an UNHARDENED target's memory (%s); "+
				"this environment refuses every read, so the hardened case below would prove nothing", o.openErr)
		}
		if !o.matched {
			t.Fatalf("the reader opened the unhardened target's memory but did not read the marker back")
		}
		if !o.warned {
			t.Fatalf("the opt-out must log a warning naming %s", AllowProcessInspectionEnv)
		}
	})

	t.Run("hardened is not", func(t *testing.T) {
		o := runHardenScenario(t, "")
		if o.dumpable != 0 {
			t.Fatalf("hardened target PR_GET_DUMPABLE = %d, want 0", o.dumpable)
		}
		if o.openErr == "" {
			t.Fatalf("a same-user reader opened the hardened target's /proc/<pid>/mem (marker read back: %t)", o.matched)
		}
		if o.warned {
			t.Fatalf("a hardened process must not log the opt-out warning")
		}
	})
}

// TestHardenProcess_ReaderHelper is READER. It is a no-op unless the role
// variable selects it.
func TestHardenProcess_ReaderHelper(t *testing.T) {
	if os.Getenv(hardenRoleEnv) != hardenRoleRead {
		return
	}
	if err := runHardenReader(); err != nil {
		fmt.Fprintln(os.Stderr, "reader:", err)
		os.Exit(2)
	}
}

// TestHardenProcess_TargetHelper is TARGET. It is a no-op unless the role
// variable selects it.
func TestHardenProcess_TargetHelper(t *testing.T) {
	if os.Getenv(hardenRoleEnv) != hardenRoleTgt {
		return
	}
	if err := runHardenTarget(); err != nil {
		fmt.Fprintln(os.Stderr, "target:", err)
		os.Exit(2)
	}
}

// runHardenTarget hardens (or not), publishes what READER needs, and holds
// the marker live until READER closes its stdin.
func runHardenTarget() error {
	marker, err := hex.DecodeString(os.Getenv(hardenMarkerEnv))
	if err != nil {
		return err
	}
	if len(marker) == 0 {
		return errNoHardenMarker
	}
	if err := HardenProcess(nil); err != nil {
		return err
	}
	dumpable, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "TARGET dumpable=%d addr=%d len=%d\n", dumpable, uintptr(unsafe.Pointer(&marker[0])), len(marker))
	_, _ = io.Copy(io.Discard, os.Stdin) // hold until READER is done
	runtime.KeepAlive(marker)
	return nil
}

// runHardenReader drops CAP_SYS_PTRACE, starts TARGET as its child, and
// reports whether TARGET's memory was readable.
func runHardenReader() error {
	// Capabilities are per-thread on Linux; the drop and every syscall that
	// depends on it (the fork of TARGET, the open, the pread) stay on this one.
	runtime.LockOSThread()
	if err := dropPtraceCapability(); err != nil {
		return err
	}

	marker := make([]byte, 32)
	if _, err := rand.Read(marker); err != nil {
		return err
	}
	// #nosec G204 -- re-exec of this test binary, as above.
	target := exec.Command(os.Args[0], hardenSubprocessArgs("TestHardenProcess_TargetHelper")...)
	target.Env = append(os.Environ(),
		hardenRoleEnv+"="+hardenRoleTgt,
		hardenMarkerEnv+"="+hex.EncodeToString(marker),
	)
	var stderr bytes.Buffer
	target.Stderr = &stderr
	stdin, err := target.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := target.StdoutPipe()
	if err != nil {
		return err
	}
	if err := target.Start(); err != nil {
		return err
	}
	defer func() {
		_ = stdin.Close()
		_ = target.Wait()
	}()

	var dumpable, length int
	var addr uint64
	scanner := bufio.NewScanner(stdout)
	found := false
	for scanner.Scan() {
		if _, serr := fmt.Sscanf(scanner.Text(), "TARGET dumpable=%d addr=%d len=%d", &dumpable, &addr, &length); serr == nil {
			found = true
			break
		}
	}
	if !found {
		_ = stdin.Close()
		_ = target.Wait()
		fmt.Fprintf(os.Stderr, "target stderr:\n%s\n", stderr.String())
		return errTargetPublishedNothing
	}

	openErr, matched := readTargetMemory(target.Process.Pid, int64(addr), length, marker) // #nosec G115 -- an address printed by the child from a live pointer
	_ = stdin.Close()
	_ = target.Wait()
	warned := strings.Contains(stderr.String(), AllowProcessInspectionEnv)
	fmt.Fprintf(os.Stdout, "OUTCOME dumpable=%d open=%s matched=%t warned=%t\n", dumpable, strconv.Quote(openErr), matched, warned)
	return nil
}

// readTargetMemory opens /proc/<pid>/mem and reads length bytes at addr. It
// returns the open error's text ("" on success) and whether the bytes read
// equal want.
func readTargetMemory(pid int, addr int64, length int, want []byte) (string, bool) {
	f, err := os.Open("/proc/" + strconv.Itoa(pid) + "/mem")
	if err != nil {
		return err.Error(), false
	}
	defer f.Close()
	got := make([]byte, length)
	if _, err := f.ReadAt(got, addr); err != nil {
		return "", false
	}
	return "", bytes.Equal(got, want)
}

// dropPtraceCapability clears CAP_SYS_PTRACE from the calling thread's
// effective set. For an unprivileged process it is already clear and this is
// a no-op.
func dropPtraceCapability() error {
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return err
	}
	const bit = unix.CAP_SYS_PTRACE
	data[bit/32].Effective &^= 1 << (bit % 32)
	return unix.Capset(&hdr, &data[0])
}
