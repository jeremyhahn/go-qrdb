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

package audit

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// emitUntilRotated emits until the sink has attempted a rotation, recovering
// a panic the rotation raises the way an upstream recover would.
func emitUntilRotated(l *ChainFileLogger, attempted *bool) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	for i := 0; i < 64 && !*attempted; i++ {
		_ = l.Emit(context.Background(), testEvent("alice", "barrier", "unseal"))
	}
	return false
}

// A panic inside a rotation -- here from the rename -- that is recovered
// upstream must not leave the rotation sequence odd. If it did, every later
// verification would read "a rotation is mid-stride" forever and answer
// *ConcurrentRotationError, masking a deleted log or a broken link for the
// life of the process.
func TestChainFileLogger_RotationPanicLeavesTheSequenceEven(t *testing.T) {
	l, _, path := rotatingLogger(t, 400)
	defer func() { _ = l.Close() }()
	emitN(t, l, 1, "unseal")

	attempted := false
	l.renameFunc = func(string, string) error {
		attempted = true
		panic("rename exploded")
	}
	if !emitUntilRotated(l, &attempted) {
		t.Fatal("the rotation's panic did not reach the caller")
	}
	if g := l.rotations.Load(); g%2 != 0 {
		t.Fatalf("rotations = %d after a recovered panic: a rotation reads as mid-stride forever", g)
	}

	// The log is then deleted: the verifier must say so, not "the trail moved".
	l.renameFunc = nil
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove the active log: %v", err)
	}
	if _, err := l.VerifyChainSet(context.Background()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("verification of a deleted log = %v, want fs.ErrNotExist", err)
	}
}

// A rotation whose rename fails, and one whose re-open fails, both end with
// the sequence even: neither leaves the verifier believing a rotation is
// still in progress.
func TestChainFileLogger_FailedRotationLeavesTheSequenceEven(t *testing.T) {
	t.Run("rename fails", func(t *testing.T) {
		l, _, _ := rotatingLogger(t, 400)
		defer func() { _ = l.Close() }()
		emitN(t, l, 1, "unseal")
		attempted := false
		l.renameFunc = func(string, string) error {
			attempted = true
			return errors.New("rename refused")
		}
		emitUntilRotated(l, &attempted)
		if !attempted {
			t.Fatal("no rotation was attempted")
		}
		if g := l.rotations.Load(); g == 0 || g%2 != 0 {
			t.Fatalf("rotations = %d after a failed rename, want a non-zero even value", g)
		}
	})
	t.Run("reopen fails", func(t *testing.T) {
		l, _, _ := rotatingLogger(t, 400)
		defer func() { _ = l.Close() }()
		emitN(t, l, 1, "unseal")
		attempted := false
		l.renameFunc = func(oldpath, newpath string) error {
			if err := fsync.Rename(oldpath, newpath); err != nil {
				return err
			}
			attempted = true
			// A directory where the active file belongs: the re-open fails.
			return os.Mkdir(oldpath, 0o700)
		}
		emitUntilRotated(l, &attempted)
		if !attempted {
			t.Fatal("no rotation was attempted")
		}
		if _, err := os.Stat(l.path); err != nil {
			t.Fatalf("the directory standing in for the active file: %v", err)
		}
		if g := l.rotations.Load(); g == 0 || g%2 != 0 {
			t.Fatalf("rotations = %d after a failed re-open, want a non-zero even value", g)
		}
	})
}
