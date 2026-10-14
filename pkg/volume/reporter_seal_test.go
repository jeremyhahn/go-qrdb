// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// fakeSealProbe is a SealProbe whose state a test flips.
type fakeSealProbe struct {
	sealed atomic.Bool
}

var _ SealProbe = (*fakeSealProbe)(nil)

func (p *fakeSealProbe) IsSealed() bool { return p.sealed.Load() }

// grantedLeases counts the leases the fake has handed out, so a test can
// prove a refused write proposed nothing at all.
func grantedLeases(l *fakeLeases) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.ttl)
}

func TestReporter_ReportRefusedWhileSealed(t *testing.T) {
	probe := &fakeSealProbe{}
	probe.sealed.Store(true)
	r, mvcc, leases := newTestReporter(t, "node-1", WithReporterSealProbe(probe))

	err := r.Report(context.Background(), NodeReport{})
	var sealedErr *NodeSealedError
	if !errors.As(err, &sealedErr) {
		t.Fatalf("Report while sealed: err = %v, want *NodeSealedError", err)
	}
	if n := grantedLeases(leases); n != 0 {
		t.Fatalf("Report while sealed granted %d lease(s); a sealed node must propose nothing", n)
	}
	if raw, _ := mvcc.Get(context.Background(), NodeReportKey("node-1")); raw != nil {
		t.Fatalf("Report while sealed wrote the record: %s", raw)
	}
}

func TestReporter_ReportResumesAfterUnseal(t *testing.T) {
	probe := &fakeSealProbe{}
	probe.sealed.Store(true)
	r, mvcc, _ := newTestReporter(t, "node-1", WithReporterSealProbe(probe))

	if err := r.Report(context.Background(), NodeReport{}); err == nil {
		t.Fatal("Report while sealed: want an error")
	}
	probe.sealed.Store(false)
	if err := r.Report(context.Background(), NodeReport{}); err != nil {
		t.Fatalf("Report after unseal: %v", err)
	}
	if raw, err := mvcc.Get(context.Background(), NodeReportKey("node-1")); err != nil || raw == nil {
		t.Fatalf("Report after unseal did not publish: raw=%v err=%v", raw, err)
	}
}

func TestReporter_StartRefusedWhileSealed(t *testing.T) {
	probe := &fakeSealProbe{}
	probe.sealed.Store(true)
	r, _, leases := newTestReporter(t, "node-1", WithReporterSealProbe(probe))

	err := r.Start(context.Background(), func() NodeReport { return NodeReport{} })
	var sealedErr *NodeSealedError
	if !errors.As(err, &sealedErr) {
		t.Fatalf("Start while sealed: err = %v, want *NodeSealedError", err)
	}
	if n := grantedLeases(leases); n != 0 {
		t.Fatalf("Start while sealed granted %d lease(s)", n)
	}
	r.mu.Lock()
	started := r.started
	r.mu.Unlock()
	if started {
		t.Fatal("Start while sealed started the refresh loop")
	}
}

func TestReporter_RefreshSkipsWhileSealed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		probe := &fakeSealProbe{}
		r, _, _ := newTestReporter(t, "node-1", WithReporterSealProbe(probe))

		var calls atomic.Int32
		build := func() NodeReport {
			calls.Add(1)
			return NodeReport{}
		}
		if err := r.Start(context.Background(), build); err != nil {
			t.Fatalf("Start: %v", err)
		}
		// Close inside the bubble even on failure: the refresh loop must
		// exit before synctest.Test returns.
		defer func() {
			if err := r.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}()

		probe.sealed.Store(true)
		time.Sleep(3*DefaultReportInterval + time.Millisecond)
		synctest.Wait()
		if got := calls.Load(); got != 1 {
			t.Fatalf("refresh ran %d time(s) while sealed; want none after the initial report", got-1)
		}

		probe.sealed.Store(false)
		time.Sleep(DefaultReportInterval)
		synctest.Wait()
		if got := calls.Load(); got != 2 {
			t.Fatalf("refresh after unseal: build calls = %d, want 2", got)
		}
	})
}

func TestReporter_RefreshRunsWithoutSealProbe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _, _ := newTestReporter(t, "node-1", WithReporterSealProbe(nil))

		var calls atomic.Int32
		build := func() NodeReport {
			calls.Add(1)
			return NodeReport{}
		}
		if err := r.Start(context.Background(), build); err != nil {
			t.Fatalf("Start with a nil probe: %v", err)
		}
		// Close inside the bubble even on failure: the refresh loop must
		// exit before synctest.Test returns.
		defer func() {
			if err := r.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}()
		time.Sleep(2*DefaultReportInterval + time.Millisecond)
		synctest.Wait()
		if got := calls.Load(); got != 3 {
			t.Fatalf("build calls = %d, want 3 (initial + 2 refreshes)", got)
		}
	})
}

func TestNodeSealedError_Error(t *testing.T) {
	err := &NodeSealedError{Op: "node report"}
	if got, want := err.Error(), "volume: node barrier is sealed: node report not proposed"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestNodeSealedError_NamesTheOperation(t *testing.T) {
	a := (&NodeSealedError{Op: "a"}).Error()
	b := (&NodeSealedError{Op: "b"}).Error()
	if a == b {
		t.Fatalf("errors for different operations are indistinguishable: %q", a)
	}
}
