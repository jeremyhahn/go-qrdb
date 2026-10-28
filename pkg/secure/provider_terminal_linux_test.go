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
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

// openTerminal opens a pseudo-terminal pair in-process and installs its
// subordinate end as os.Stdin and a temporary file as os.Stderr, restoring
// both when the test ends. The input written to the returned controlling
// end is queued by the line discipline before Passphrase reads it, so no
// test below waits on anything.
func openTerminal(t *testing.T) (ctrl *os.File, stderr *os.File) {
	t.Helper()
	ctrl, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { _ = ctrl.Close() })
	if err := unix.IoctlSetPointerInt(int(ctrl.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlock the pseudo-terminal: %v", err)
	}
	n, err := unix.IoctlGetInt(int(ctrl.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("pseudo-terminal number: %v", err)
	}
	sub, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open the subordinate terminal: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })

	stderr, err = os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatalf("create stderr file: %v", err)
	}
	t.Cleanup(func() { _ = stderr.Close() })

	origIn, origErr := os.Stdin, os.Stderr
	os.Stdin, os.Stderr = sub, stderr
	t.Cleanup(func() { os.Stdin, os.Stderr = origIn, origErr })
	return ctrl, stderr
}

func typeInput(t *testing.T, ctrl *os.File, input string) {
	t.Helper()
	if _, err := ctrl.WriteString(input); err != nil {
		t.Fatalf("write terminal input: %v", err)
	}
}

func TestTerminalProvider_ReadsPassphraseFromTerminal(t *testing.T) {
	ctrl, stderr := openTerminal(t)
	typeInput(t, ctrl, "correct horse\n")

	buf, err := NewTerminalProvider().Passphrase(context.Background(), "Passphrase: ")
	if err != nil {
		t.Fatalf("Passphrase: %v", err)
	}
	defer buf.Destroy()
	if err := buf.WithBytes(func(b []byte) error {
		if string(b) != "correct horse" {
			t.Errorf("passphrase: got %q, want %q", b, "correct horse")
		}
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
	prompt, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if string(prompt) != "Passphrase: \n" {
		t.Errorf("stderr: got %q, want the prompt and the newline echo-off swallowed", prompt)
	}
}

func TestTerminalProvider_EmptyLineRefused(t *testing.T) {
	ctrl, _ := openTerminal(t)
	typeInput(t, ctrl, "\n")

	buf, err := NewTerminalProvider().Passphrase(context.Background(), "")
	var pe *ProviderError
	if buf != nil || !errors.As(err, &pe) || !errors.Is(err, errEmptyPassphrase) {
		t.Fatalf("expected *ProviderError wrapping errEmptyPassphrase, got buf=%v err=%v", buf, err)
	}
}

func TestTerminalProvider_PromptWriteFailure(t *testing.T) {
	_, stderr := openTerminal(t)
	if err := stderr.Close(); err != nil {
		t.Fatalf("close stderr: %v", err)
	}

	buf, err := NewTerminalProvider().Passphrase(context.Background(), "Passphrase: ")
	var pe *ProviderError
	if buf != nil || !errors.As(err, &pe) || !errors.Is(err, os.ErrClosed) {
		t.Fatalf("expected *ProviderError wrapping os.ErrClosed, got buf=%v err=%v", buf, err)
	}
}
