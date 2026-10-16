// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package lease

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// recordingLessor returns a lessor whose renewal marker the checkpoint
// applied at index 5 set, so its renewals are recorded, and a lease on it.
func recordingLessor(t *testing.T) (*Lessor, int64) {
	t.Helper()
	lr := NewLessor()
	l, err := lr.Grant(nopWriter{}, 60)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	lr.Applying(5)
	if err := lr.ApplyCheckpoints(nopWriter{}, 0, 5, nil); err != nil {
		t.Fatalf("ApplyCheckpoints setting the marker: %v", err)
	}
	if lr.recordFrom.Load() != 5 {
		t.Fatalf("renewal marker = %d, want 5", lr.recordFrom.Load())
	}
	return lr, l.ID
}

func TestKeepAlive_RenewalRecordWriteFaultIsReported(t *testing.T) {
	lr, id := recordingLessor(t)
	lr.Applying(6)
	var twe *TableWriteError
	if _, err := lr.KeepAlive(faultWriter{}, id); !errors.As(err, &twe) || !errors.Is(err, errWriteFault) {
		t.Fatalf("KeepAlive with a failing renewal write = %v, want *TableWriteError wrapping the fault", err)
	}
	if got := lr.leases[id].renewIndex; got != 0 {
		t.Fatalf("a failed renewal write left renewIndex = %d, want 0", got)
	}
}

func TestKeepAlive_RecordsTheApplyingIndex(t *testing.T) {
	lr, id := recordingLessor(t)
	lr.Applying(9)
	if _, err := lr.KeepAlive(nopWriter{}, id); err != nil {
		t.Fatalf("KeepAlive: %v", err)
	}
	if got := lr.leases[id].renewIndex; got != 9 {
		t.Fatalf("renewIndex = %d, want 9", got)
	}
}

func TestRevoke_RecordDeleteFaultsAreReported(t *testing.T) {
	cases := map[string]func(t *testing.T, lr *Lessor, id int64){
		"renewal record": func(t *testing.T, lr *Lessor, id int64) {
			lr.Applying(6)
			if _, err := lr.KeepAlive(nopWriter{}, id); err != nil {
				t.Fatalf("KeepAlive: %v", err)
			}
		},
		"checkpoint record": func(t *testing.T, lr *Lessor, id int64) {
			lr.Applying(7)
			if err := lr.ApplyCheckpoints(nopWriter{}, 0, 6, []Checkpoint{{ID: id, RemainingTTL: 10}}); err != nil {
				t.Fatalf("ApplyCheckpoints: %v", err)
			}
			if lr.leases[id].checkpointTTL == 0 {
				t.Fatal("the unsuperseded checkpoint was not applied")
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			lr, id := recordingLessor(t)
			setup(t, lr, id)
			var twe *TableWriteError
			if _, err := lr.Revoke(faultWriter{}, id); !errors.As(err, &twe) || !errors.Is(err, errWriteFault) {
				t.Fatalf("Revoke with a failing %s delete = %v, want *TableWriteError wrapping the fault", name, err)
			}
			if _, ok := lr.leases[id]; !ok {
				t.Fatal("a failed revoke dropped the lease from memory")
			}
		})
	}
}

func TestApplyCheckpoints_MarkerWriteFaultIsReported(t *testing.T) {
	lr := NewLessor()
	lr.Applying(3)
	var twe *TableWriteError
	if err := lr.ApplyCheckpoints(faultWriter{}, 0, 3, nil); !errors.As(err, &twe) || !errors.Is(err, errWriteFault) {
		t.Fatalf("ApplyCheckpoints with a failing marker write = %v, want *TableWriteError wrapping the fault", err)
	}
	if lr.recordFrom.Load() != 0 {
		t.Fatal("a failed marker write started recording renewals")
	}
}

// nthGetFaultReader fails its nth Get and reads an empty table otherwise.
type nthGetFaultReader struct {
	n, seen int
}

func (r *nthGetFaultReader) Get(_ []byte) ([]byte, error) {
	r.seen++
	if r.seen == r.n {
		return nil, errReadFault
	}
	return nil, nil
}

func (r *nthGetFaultReader) NewIterator(_ *storage.IteratorOptions) storage.Iterator {
	return faultIterator{}
}

func TestRestore_RenewalMarkerReadFaultIsReported(t *testing.T) {
	for _, n := range []int{1, 2} {
		lr := NewLessor()
		var tre *TableReadError
		if err := lr.Restore(&nthGetFaultReader{n: n}); !errors.As(err, &tre) || !errors.Is(err, errReadFault) {
			t.Errorf("Restore failing Get %d = %v, want *TableReadError wrapping the fault", n, err)
		}
	}
}

func TestProposeCheckpoints_LogsAndReturnsAProposeFailure(t *testing.T) {
	proposeErr := errors.New("propose refused")
	cps := []Checkpoint{{ID: 1, RemainingTTL: 10}}
	for name, logger := range map[string]*slog.Logger{
		"with logger":    slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})),
		"without logger": nil,
	} {
		t.Run(name, func(t *testing.T) {
			cp := &applyingCheckpointer{err: proposeErr}
			lr := NewLessor()
			if err := lr.proposeCheckpoints(context.Background(), cp, 1, 4, cps, logger); !errors.Is(err, proposeErr) {
				t.Fatalf("proposeCheckpoints = %v, want the propose error", err)
			}
			if len(cp.rounds) != 1 || cp.rounds[0].index != 4 {
				t.Fatalf("proposed rounds = %+v, want one sampled at index 4", cp.rounds)
			}
		})
	}
}

func TestRenewalErrorMessages(t *testing.T) {
	for _, err := range []error{&NotPrimaryError{}, &StaleTTLError{Lease: &Lease{ID: 42}}} {
		if err.Error() == "" {
			t.Errorf("%T has an empty message", err)
		}
	}
	if msg := (&StaleTTLError{Lease: &Lease{ID: 42}}).Error(); !strings.Contains(msg, "42") {
		t.Errorf("StaleTTLError message %q does not name the lease", msg)
	}
}
