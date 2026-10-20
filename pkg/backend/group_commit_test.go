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

package backend

import (
	"bytes"
	"context"
	stderrors "errors"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// slowSyncSM is an SMAdapter whose Update takes syncDelay, standing in for the
// fsync a persistent engine's commit waits on. It records every group it was
// handed and marks a command synced only when the Update carrying it is about
// to return, so a proposer that sees its result before then was acknowledged
// ahead of its fsync.
type slowSyncSM struct {
	syncDelay time.Duration
	// gate, when set, holds every Update until it is closed, so a test can
	// keep the first sync in flight while it queues the rest.
	gate chan struct{}

	mu        sync.Mutex
	failWith  error
	panicWith any
	groups    [][]sm.Entry
	synced    map[string]bool

	inUpdate   atomic.Int32
	overlapped atomic.Bool
}

var _ SMAdapter = (*slowSyncSM)(nil)

func newSlowSyncSM(delay time.Duration) *slowSyncSM {
	return &slowSyncSM{syncDelay: delay, synced: make(map[string]bool)}
}

func (s *slowSyncSM) Update(ctx context.Context, entries []sm.Entry, results []sm.Result) error {
	if s.inUpdate.Add(1) > 1 {
		s.overlapped.Store(true)
	}
	defer s.inUpdate.Add(-1)
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.gate != nil {
		<-s.gate
	}
	time.Sleep(s.syncDelay)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.panicWith != nil {
		panic(s.panicWith)
	}
	if s.failWith != nil {
		return s.failWith
	}
	s.groups = append(s.groups, slices.Clone(entries))
	for i, e := range entries {
		s.synced[string(e.Cmd)] = true
		results[i] = sm.Result{Value: e.Index, Data: e.Cmd}
	}
	return nil
}

func (s *slowSyncSM) Lookup(context.Context, interface{}) (interface{}, error) { return nil, nil }

func (s *slowSyncSM) Close(context.Context) error { return nil }

func (s *slowSyncSM) isSynced(cmd []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.synced[string(cmd)]
}

func (s *slowSyncSM) groupCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.groups)
}

func (s *slowSyncSM) setPanic(v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panicWith = v
}

// proposeOutcome is what one concurrent proposer saw.
type proposeOutcome struct {
	cmd      []byte
	result   sm.Result
	err      error
	panicked any
	// syncedAtAck is whether the command's Update had completed when the
	// proposer was handed its answer.
	syncedAtAck bool
}

// proposeConcurrently releases n proposers at once against lb and returns
// what each saw and how long the whole set took.
func proposeConcurrently(t *testing.T, lb *LocalBackend, s *slowSyncSM, n int) []proposeOutcome {
	t.Helper()
	outcomes := make([]proposeOutcome, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		outcomes[i].cmd = []byte("cmd-" + strconv.Itoa(i))
		wg.Go(func() {
			o := &outcomes[i]
			defer func() { o.panicked = recover() }()
			<-start
			o.result, o.err = lb.SyncPropose(context.Background(), 1, o.cmd)
			o.syncedAtAck = s.isSynced(o.cmd)
		})
	}
	close(start)
	wg.Wait()
	return outcomes
}

func TestLocalBackend_GroupCommit_ConcurrentProposalsShareOneSync(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const n = 32
		s := newSlowSyncSM(100 * time.Millisecond)
		s.gate = make(chan struct{})
		lb := NewLocalBackend(s)
		defer func() { _ = lb.Close() }()

		var outcomes []proposeOutcome
		done := make(chan struct{})
		go func() {
			defer close(done)
			outcomes = proposeConcurrently(t, lb, s, n)
		}()
		// Every proposer is now durably blocked: the token holder inside the
		// gated Update, everyone else queued behind it waiting for the token
		// or for its group to finish.
		synctest.Wait()
		close(s.gate)
		<-done

		// One proposal per sync would take n Updates. Group commit takes the
		// first token holder's group plus one more for everyone queued while
		// it was in flight, whatever the scheduler did.
		groups, applied := s.groupCount(), 0
		s.mu.Lock()
		for _, g := range s.groups {
			applied += len(g)
		}
		s.mu.Unlock()
		if groups < 1 || groups > 2 {
			t.Fatalf("%d proposals queued behind one in-flight sync went through %d Updates, want 1 or 2", n, groups)
		}
		if applied != n {
			t.Fatalf("Updates applied %d entries, want each of the %d proposals exactly once", applied, n)
		}
		if s.overlapped.Load() {
			t.Fatal("two Updates ran at once: applies must stay sequential")
		}
		seen := make(map[uint64]bool, n)
		for _, o := range outcomes {
			if o.err != nil || o.panicked != nil {
				t.Fatalf("proposal %s: err=%v panic=%v", o.cmd, o.err, o.panicked)
			}
			if !bytes.Equal(o.result.Data, o.cmd) {
				t.Fatalf("proposal %s was handed the result of %s", o.cmd, o.result.Data)
			}
			seen[o.result.Value] = true
		}
		for idx := uint64(1); idx <= n; idx++ {
			if !seen[idx] {
				t.Fatalf("index %d was never assigned; indices must be consecutive from 1", idx)
			}
		}
	})
}

func TestLocalBackend_GroupCommit_NoAckBeforeItsSync(t *testing.T) {
	s := newSlowSyncSM(20 * time.Millisecond)
	lb := NewLocalBackend(s)
	defer func() { _ = lb.Close() }()

	outcomes := proposeConcurrently(t, lb, s, 16)
	for _, o := range outcomes {
		if o.err != nil || o.panicked != nil {
			t.Fatalf("proposal %s: err=%v panic=%v", o.cmd, o.err, o.panicked)
		}
		if !o.syncedAtAck {
			t.Fatalf("proposal %s was acknowledged before the Update covering it returned", o.cmd)
		}
	}
}

func TestLocalBackend_GroupCommit_LoneProposalIsAGroupOfOne(t *testing.T) {
	s := newSlowSyncSM(0)
	lb := NewLocalBackend(s)
	defer func() { _ = lb.Close() }()

	for i := range 3 {
		cmd := []byte("solo-" + strconv.Itoa(i))
		res, err := lb.SyncPropose(context.Background(), 1, cmd)
		if err != nil {
			t.Fatalf("SyncPropose: %v", err)
		}
		if res.Value != uint64(i+1) || !bytes.Equal(res.Data, cmd) {
			t.Fatalf("result = %+v, want index %d and %s", res, i+1, cmd)
		}
	}
	if got := s.groupCount(); got != 3 {
		t.Fatalf("3 sequential proposals made %d Updates, want 3", got)
	}
}

func TestLocalBackend_GroupCommit_UpdateErrorFailsTheWholeGroup(t *testing.T) {
	failure := stderrors.New("fsync: input/output error")
	s := newSlowSyncSM(50 * time.Millisecond)
	s.failWith = failure
	lb := NewLocalBackend(s)
	defer func() { _ = lb.Close() }()

	outcomes := proposeConcurrently(t, lb, s, 16)
	for _, o := range outcomes {
		if !stderrors.Is(o.err, failure) {
			t.Fatalf("proposal %s: err = %v, want the failed commit's error", o.cmd, o.err)
		}
		if o.syncedAtAck {
			t.Fatalf("proposal %s was marked synced by a failed commit", o.cmd)
		}
	}
}

func TestLocalBackend_GroupCommit_PanicCompletesTheGroupAndReleasesTheToken(t *testing.T) {
	s := newSlowSyncSM(50 * time.Millisecond)
	s.setPanic("state machine bug")
	lb := NewLocalBackend(s)
	defer func() { _ = lb.Close() }()

	outcomes := proposeConcurrently(t, lb, s, 16)
	for _, o := range outcomes {
		var aborted *ApplyAbortedError
		if o.panicked == nil && !stderrors.As(o.err, &aborted) {
			t.Fatalf("proposal %s neither applied the panicking group nor got ApplyAbortedError: err=%v", o.cmd, o.err)
		}
	}

	s.setPanic(nil)
	done := make(chan error, 1)
	go func() {
		_, err := lb.SyncPropose(context.Background(), 1, []byte("after"))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SyncPropose after the panic: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SyncPropose after a panicking Update never returned: the applier token was not released")
	}
}

func TestLocalBackend_GroupCommit_CancelledProposerStillAppliesItsGroup(t *testing.T) {
	s := newSlowSyncSM(0)
	lb := NewLocalBackend(s)
	defer func() { _ = lb.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lb.SyncPropose(ctx, 1, []byte("cancelled")); err != nil {
		t.Fatalf("SyncPropose with a cancelled context: %v; a group is applied whatever its token holder's context", err)
	}
	if !s.isSynced([]byte("cancelled")) {
		t.Fatal("the proposal was acknowledged but never applied")
	}
}

func TestApplyAbortedError_Error(t *testing.T) {
	if got := (&ApplyAbortedError{}).Error(); got == "" {
		t.Fatal("empty error string")
	}
}
