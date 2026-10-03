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
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// TestChainFileLogger_VerifyChainSet_NeverReportsTheRotationGapAsAMissingLog.
//
// A rotation renames the active file to its archive name and then opens a
// fresh one, under the writer's lock -- and the verifier does not take that
// lock. Between the rename and the re-open the active name does not exist. A
// writer descheduled in that window under host load for longer than the
// verifier's whole retry budget used to make every attempt see the same
// absence, and the verifier then reported the active log missing -- a fault
// verdict on an honest trail, which is the one thing it must never give.
//
// The window is held open here deterministically: the rename seam performs the
// real rename and then waits, holding the writer's lock exactly as a stalled
// rotation would, while the verifier runs to completion. Its answer must be
// that the trail moved (*ConcurrentRotationError), never that the log is
// missing; and once the rotation finishes the trail must verify clean.
func TestChainFileLogger_VerifyChainSet_NeverReportsTheRotationGapAsAMissingLog(t *testing.T) {
	l, _, _ := rotatingLogger(t, 400)
	defer func() { _ = l.Close() }()
	emitN(t, l, 1, "unseal") // a non-empty active file, so the first rotation has a record to hand off

	inGap := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	l.renameFunc = func(oldpath, newpath string) error {
		err := fsync.Rename(oldpath, newpath)
		once.Do(func() {
			close(inGap)
			<-release
		})
		return err
	}

	ctx := context.Background()
	emitted := make(chan error, 1)
	go func() {
		// Emit until the sink rotates; the first rotation stalls in the gap.
		for i := 0; i < 64; i++ {
			if err := l.Emit(ctx, testEvent("alice", "barrier", "unseal")); err != nil {
				emitted <- err
				return
			}
		}
		emitted <- nil
	}()
	<-inGap

	_, err := l.VerifyChainSet(ctx)
	close(release)
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("verification inside a rotation reported the active log missing: %v", err)
	}
	var raced *ConcurrentRotationError
	if !errors.As(err, &raced) {
		t.Fatalf("verification inside a rotation = %v, want *ConcurrentRotationError", err)
	}

	if err := <-emitted; err != nil {
		t.Fatalf("emit: %v", err)
	}
	if _, err := l.VerifyChainSet(ctx); err != nil {
		t.Fatalf("the finished trail must verify: %v", err)
	}
}

// A generation that moves between an attempt's two reads is a rotation that
// began or ended while it looked, even when both listings agree: every such
// attempt is a race, and a walk that never sees a still generation reports
// the trail moved -- never a verdict on it.
func TestVerifyChainSetAt_AMovingGenerationIsARace(t *testing.T) {
	l, _, path := rotatingLogger(t, 400)
	emitN(t, l, 3, "unseal")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var gen uint64
	moving := func() uint64 { gen += 2; return gen }
	var raced *ConcurrentRotationError
	if _, err := verifyChainSetAt(path, moving); !errors.As(err, &raced) {
		t.Fatalf("verifyChainSetAt with a moving generation = %v, want *ConcurrentRotationError", err)
	}

	still := func() uint64 { return 4 }
	if _, err := verifyChainSetAt(path, still); err != nil {
		t.Fatalf("verifyChainSetAt with a still, even generation = %v, want a clean verification", err)
	}
}
