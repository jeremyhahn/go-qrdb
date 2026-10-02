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

package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
)

// periodTargets is one database with a period policy of period.
func periodTargets(period string) []compactionTarget {
	return []compactionTarget{{Tenant: "acme", Database: "app",
		Policy: &compaction.Policy{Mode: compaction.Period, RetainPeriod: period}}}
}

// TestCompactionScheduler_PeriodSamplesAreBounded: a period policy keeps at
// most about periodSampleResolution samples however often the scheduler
// runs -- one pass a second over three hours of a one-hour policy is 10800
// passes, not 10800 samples.
func TestCompactionScheduler_PeriodSamplesAreBounded(t *testing.T) {
	view := &schedMVCC{current: 1}
	reg := &schedRegistry{views: map[string]*schedMVCC{"acme/app": view}}
	now := time.Unix(1_700_000_000, 0)
	s := newSched(reg, periodTargets("1h"), func() time.Time { return now })

	most := 0
	for i := range 3 * 3600 {
		now = now.Add(time.Second)
		view.mu.Lock()
		view.current = int64(i + 2)
		view.mu.Unlock()
		s.passOnce(context.Background())
		most = max(most, len(s.samples["acme/app"]))
	}
	if most > periodSampleResolution+2 {
		t.Fatalf("a one-hour period policy held %d samples; the bound is about %d", most, periodSampleResolution)
	}
	// Still compacting, to a revision at most one sample interval older than
	// the period: the cap costs precision on the side of keeping history.
	calls := view.calls()
	if len(calls) == 0 {
		t.Fatal("a bounded period policy stopped compacting")
	}
	last, current := calls[len(calls)-1], int64(3*3600+1)
	if age := current - last; age < 3600 || age > 3600+3600/periodSampleResolution+1 {
		t.Fatalf("last compaction to %d at current %d keeps %d revisions (one a second); want one hour to one hour and a sample interval", last, current, age)
	}
}

// TestCompactionScheduler_PeriodSamplesSurviveARestart: the samples are
// persisted, so a node that restarts more often than its period still
// compacts -- the clock does not start over at every start.
func TestCompactionScheduler_PeriodSamplesSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), compactionSamplesFile)
	view := &schedMVCC{current: 10}
	reg := &schedRegistry{views: map[string]*schedMVCC{"acme/app": view}}
	start := time.Unix(1_700_000_000, 0)

	first := newSched(reg, periodTargets("1h"), fixedClock(start))
	first.samplesPath = path
	first.passOnce(context.Background())
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no samples file after a pass that took a sample: %v", err)
	}

	// Restart after 40 minutes, then again after another 40: neither run
	// alone ever saw a sample a full period old.
	restarted := func(at time.Time, current int64) int {
		s := newSched(reg, periodTargets("1h"), fixedClock(at))
		s.samplesPath = path
		if err := s.loadSamples(); err != nil {
			t.Fatalf("loadSamples: %v", err)
		}
		view.mu.Lock()
		view.current = current
		view.mu.Unlock()
		return s.passOnce(context.Background())
	}
	if n := restarted(start.Add(40*time.Minute), 20); n != 0 {
		t.Fatal("compacted before a sample was a period old")
	}
	if n := restarted(start.Add(80*time.Minute), 30); n != 1 {
		t.Fatalf("after two restarts and 80 minutes compacted %d, want 1", n)
	}
	if calls := view.calls(); len(calls) != 1 || calls[0] != 10 {
		t.Fatalf("Compact calls = %v, want [10], the revision sampled before both restarts", calls)
	}
}

// TestCompactionScheduler_SamplesFileErrors: a missing file is no samples; an
// unreadable one is a typed error naming it, and a scheduler that cannot load
// starts with none -- it waits a period rather than guessing.
func TestCompactionScheduler_SamplesFileErrors(t *testing.T) {
	dir := t.TempDir()
	s := newSched(&schedRegistry{}, nil, fixedClock(time.Now()))

	s.samplesPath = filepath.Join(dir, "absent.json")
	if err := s.loadSamples(); err != nil || len(s.samples) != 0 {
		t.Fatalf("missing file = %v, %v", s.samples, err)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.samplesPath = bad
	var se *CompactionSamplesError
	if err := s.loadSamples(); !errors.As(err, &se) || se.Path != bad || len(s.samples) != 0 {
		t.Fatalf("corrupt file = %v, samples %v; want a CompactionSamplesError naming it and no samples", err, s.samples)
	}
	if se.Error() == "" || se.Unwrap() == nil {
		t.Fatalf("CompactionSamplesError = %q, unwrap %v", se.Error(), se.Unwrap())
	}

	// A save into a directory that does not exist is the same typed error.
	s.samplesPath = filepath.Join(dir, "missing-dir", "samples.json")
	s.samples["acme/app"] = []revisionSample{{at: time.Now(), revision: 1}}
	if err := s.saveSamples(); !errors.As(err, &se) {
		t.Fatalf("save into a missing directory = %v", err)
	}
	// No path: nothing persisted, no error.
	s.samplesPath = ""
	if err := s.saveSamples(); err != nil {
		t.Fatalf("save with no path = %v", err)
	}
	if err := s.loadSamples(); err != nil {
		t.Fatalf("load with no path = %v", err)
	}
}
