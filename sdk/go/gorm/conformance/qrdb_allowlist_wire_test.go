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

//go:build gormconformance

package tests_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// TestAllowlistWireFormatCarriesEveryField is the guard for a failure mode
// that already happened once and was expensive to find.
//
// runner.go is a SEPARATE PROCESS. It re-declares its own struct and decodes
// the JSON this package dumps. Nothing makes the two definitions agree: a
// field added here and not there is ignored, and a field dropped here but
// still read there decodes to its zero value in complete silence.
//
// On 2026-08-15 an allowlist rewrite dropped AbortsRun and Unstable from the
// dumped struct while runner.go still read aborts_run and unstable. Every
// run-aborting test silently stopped being excluded, and the node target went
// from 396 tests to 139 while still reporting "0 unexpected failures" -- a
// gate that looked greener the more of the suite it skipped.
//
// This test fails if a field of expectedFailure is not present in the dumped
// JSON, so the next such rewrite breaks here instead of in the ratchet.
func TestAllowlistWireFormatCarriesEveryField(t *testing.T) {
	probe := expectedFailure{
		Test:      "TestProbe",
		Targets:   []target{targetNode},
		Reason:    "probe",
		Issue:     "probe",
		Retire:    time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
		AbortsRun: true,
		Unstable:  true,
	}

	// Marshal through the same shape dumpAllowlistAndExit emits.
	encoded, err := json.Marshal(allowlistWireForm([]expectedFailure{probe}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded []map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("decoded %d entries, want 1", len(decoded))
	}
	got := decoded[0]

	// Every exported field of expectedFailure must reach the wire. The
	// mapping is spelled out rather than derived so that adding a field
	// without deciding its JSON name is a compile error here.
	wantKeys := map[string]string{
		"Test":      "test",
		"Targets":   "targets",
		"Reason":    "reason",
		"Issue":     "issue",
		"Retire":    "retire",
		"AbortsRun": "aborts_run",
		"Unstable":  "unstable",
	}

	structType := reflect.TypeOf(expectedFailure{})
	if structType.NumField() != len(wantKeys) {
		t.Fatalf("expectedFailure has %d fields but %d are mapped to JSON; "+
			"a new field must be added to wireFailure in dumpAllowlistAndExit, to "+
			"runner.go's own struct, and to wantKeys here",
			structType.NumField(), len(wantKeys))
	}

	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i).Name
		key, mapped := wantKeys[field]
		if !mapped {
			t.Errorf("field %s has no JSON mapping declared in this test", field)
			continue
		}
		if _, present := got[key]; !present {
			t.Errorf("field %s (json %q) is not emitted by the allowlist dump; "+
				"runner.go will decode it as its zero value and silently lose it", field, key)
		}
	}

	// The two booleans are the ones that were lost, so assert their VALUES
	// survive rather than only their keys -- a key emitted with a hardcoded
	// false would pass a presence check and still break the ratchet.
	if got["aborts_run"] != true {
		t.Errorf("aborts_run = %v, want true; run-aborting tests would stop being skipped", got["aborts_run"])
	}
	if got["unstable"] != true {
		t.Errorf("unstable = %v, want true; order-dependent tests would fail the ratchet as stale", got["unstable"])
	}
}

// TestAllowlistEntriesAreWellFormed checks the invariants runner.go assumes
// but cannot report clearly: an entry with no target applies to nothing, and
// an entry with no Retire is a permanent exemption, which the ratchet
// explicitly is not.
func TestAllowlistEntriesAreWellFormed(t *testing.T) {
	seen := make(map[string]map[target]bool)

	for _, entry := range allowlist {
		if entry.Test == "" {
			t.Error("an allowlist entry has an empty Test name")
			continue
		}
		if len(entry.Targets) == 0 {
			t.Errorf("%s: no Targets, so the entry excuses nothing and is dead weight", entry.Test)
		}
		if entry.Retire.IsZero() {
			t.Errorf("%s: no Retire date; an allowlist entry is a promise to come back", entry.Test)
		}
		if entry.Reason == "" {
			t.Errorf("%s: no Reason category", entry.Test)
		}
		if entry.Issue == "" {
			t.Errorf("%s: no Issue text saying what actually fails", entry.Test)
		}

		// The same test may legitimately appear more than once with
		// DISJOINT targets (it can abort on one wire and merely fail on
		// another). Overlapping targets are a duplicate the runner would
		// double-count against the budget.
		for _, tgt := range entry.Targets {
			if seen[entry.Test] == nil {
				seen[entry.Test] = make(map[target]bool)
			}
			if seen[entry.Test][tgt] {
				t.Errorf("%s is listed twice for target %s", entry.Test, tgt)
			}
			seen[entry.Test][tgt] = true
		}
	}
}
