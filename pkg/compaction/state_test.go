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

package compaction

import (
	"errors"
	"testing"
	"time"
)

func TestRecordKey_IsReservedAndScoped(t *testing.T) {
	for _, scope := range []string{"acme/app/", "", NodeScope} {
		key := RecordKey(scope)
		if key[0] != 0x00 || !IsTableKey(key) {
			t.Fatalf("RecordKey(%q) = %q: not a reserved table key", scope, key)
		}
		if got, ok := ScopeOf(key); !ok || got != scope {
			t.Fatalf("ScopeOf(RecordKey(%q)) = %q, %v", scope, got, ok)
		}
	}
	if IsTableKey([]byte("\x00qrdb.lease/seq")) || IsTableKey([]byte("acme/app/k")) {
		t.Fatal("a key outside the table is a table key")
	}
	if _, ok := ScopeOf([]byte("acme/app/k")); ok {
		t.Fatal("ScopeOf accepted a key outside the table")
	}
}

func TestState_EncodeDecodeRoundTrip(t *testing.T) {
	s := &State{
		Policy:  &Policy{Mode: Period, RetainPeriod: "1h"},
		Samples: []Sample{{At: 10, Revision: 3}, {At: 20, Revision: 9}},
	}
	data, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeState(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Policy.Mode != Period || got.Policy.RetainPeriod != "1h" || len(got.Samples) != 2 || got.Samples[1] != (Sample{At: 20, Revision: 9}) {
		t.Fatalf("round trip = %+v", got)
	}
	var corrupt *CorruptStateError
	if _, err := DecodeState([]byte("{nope")); !errors.As(err, &corrupt) || corrupt.Error() == "" || corrupt.Unwrap() == nil {
		t.Fatalf("DecodeState(corrupt) = %v", err)
	}
}

// TestState_RecordCapsSamplesPerPeriod: a sample is kept only once the newest
// is period/SampleResolution old, and samples older than the newest one a
// whole period old are dropped -- so a state holds about SampleResolution
// samples however often it is sampled.
func TestState_RecordCapsSamplesPerPeriod(t *testing.T) {
	period := time.Hour
	s := &State{Policy: &Policy{Mode: Period, RetainPeriod: period.String()}}
	start := time.Unix(1_700_000_000, 0).UnixNano()
	most := 0
	for i := range 3 * 3600 {
		s.Record(start+int64(i)*int64(time.Second), int64(i+1))
		most = max(most, len(s.Samples))
	}
	if most > SampleResolution+2 {
		t.Fatalf("one sample a second for three hours of a one-hour policy held %d samples; the bound is about %d", most, SampleResolution)
	}
	if !s.Record(start+int64(4*3600)*int64(time.Second), 5) {
		t.Fatal("a sample an interval after the newest was refused")
	}
	if s.Record(start+int64(4*3600)*int64(time.Second)+1, 6) {
		t.Fatal("a sample within the interval was recorded")
	}
	if s.Record(0, 7) {
		t.Fatal("a sample older than the newest was recorded")
	}
}

func TestState_RecordIgnoresAPolicyWithoutPeriod(t *testing.T) {
	for _, p := range []*Policy{nil, {Mode: Revision, RetainRevisions: 5}} {
		s := &State{Policy: p}
		if s.Record(1, 1) || len(s.Samples) != 0 {
			t.Fatalf("policy %+v recorded a sample", p)
		}
	}
}

func TestState_Target(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	rev := &State{Policy: &Policy{Mode: Revision, RetainRevisions: 10}}
	if got := rev.Target(now, 100); got != 90 {
		t.Fatalf("revision target = %d, want 90", got)
	}

	per := &State{Policy: &Policy{Mode: Period, RetainPeriod: "1h"}, Samples: []Sample{
		{At: now.Add(-3 * time.Hour).UnixNano(), Revision: 10},
		{At: now.Add(-90 * time.Minute).UnixNano(), Revision: 20},
		{At: now.Add(-30 * time.Minute).UnixNano(), Revision: 30},
	}}
	if got := per.Target(now, 40); got != 20 {
		t.Fatalf("period target = %d, want 20 (the newest sample at least an hour old)", got)
	}
	fresh := &State{Policy: &Policy{Mode: Period, RetainPeriod: "1h"}, Samples: []Sample{{At: now.UnixNano(), Revision: 5}}}
	if got := fresh.Target(now, 5); got != 0 {
		t.Fatalf("period target with no sample a period old = %d, want 0", got)
	}
	if got := (&State{}).Target(now, 5); got != 0 {
		t.Fatalf("target with no policy = %d", got)
	}
}

// TestState_BindReplacesAnotherIncarnation: binding a policy for another
// incarnation of the database -- dropped and created again under the name --
// discards the old incarnation's samples; re-binding the same incarnation
// keeps them.
func TestState_BindReplacesAnotherIncarnation(t *testing.T) {
	period := &Policy{Mode: Period, RetainPeriod: "1h"}
	s := &State{}
	s.Bind(Binding{Policy: period, Tenant: "acme", Database: "app", Incarnation: "one"})
	s.Samples = []Sample{{At: 1, Revision: 1}}

	s.Bind(Binding{Policy: &Policy{Mode: Period, RetainPeriod: "2h"}, Tenant: "acme", Database: "app", Incarnation: "one"})
	if len(s.Samples) != 1 || s.Policy.RetainPeriod != "2h" {
		t.Fatalf("re-binding the same incarnation = %+v, want the samples kept", s)
	}
	s.Bind(Binding{Policy: period, Tenant: "acme", Database: "app", Incarnation: "two"})
	if len(s.Samples) != 0 || s.Incarnation != "two" {
		t.Fatalf("binding another incarnation = %+v, want its samples gone", s)
	}
	s.Samples = []Sample{{At: 1, Revision: 1}}
	s.Bind(Binding{Policy: &Policy{Mode: Revision, RetainRevisions: 3}, Tenant: "acme", Database: "app", Incarnation: "two"})
	if len(s.Samples) != 0 {
		t.Fatalf("a revision policy kept samples: %+v", s)
	}
}

// TestState_RecordStartsOverWhenRevisionsDo: a sample below the newest one's
// revision means the store's revisions started over (a restore, a re-created
// store); the old samples describe revisions it no longer has.
func TestState_RecordStartsOverWhenRevisionsDo(t *testing.T) {
	s := &State{Policy: &Policy{Mode: Period, RetainPeriod: "1h"}, Samples: []Sample{
		{At: int64(time.Hour), Revision: 500},
	}}
	if !s.Record(int64(2*time.Hour), 3) {
		t.Fatal("a sample after the revisions started over was refused")
	}
	if len(s.Samples) != 1 || s.Samples[0].Revision != 3 {
		t.Fatalf("samples = %+v, want only the new one", s.Samples)
	}

	// A delayed proposal -- older in time, lower in revision -- is ignored,
	// not mistaken for the revisions starting over.
	s = &State{Policy: &Policy{Mode: Period, RetainPeriod: "1h"}, Samples: []Sample{
		{At: int64(time.Hour), Revision: 100},
		{At: int64(2 * time.Hour), Revision: 200},
	}}
	if s.Record(int64(90*time.Minute), 150) || len(s.Samples) != 2 {
		t.Fatalf("a delayed sample changed the series: %+v", s.Samples)
	}
}

// TestState_UnusableSamplesAreNoTarget: samples from the future, out of
// order, or above the store's current revision never become a target -- the
// reviewer's probe compacted a re-created database to a revision written a
// second earlier, under a one-hour policy, from such samples.
func TestState_UnusableSamplesAreNoTarget(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	old := now.Add(-2 * time.Hour).UnixNano()
	policy := &Policy{Mode: Period, RetainPeriod: "1h"}
	for name, samples := range map[string][]Sample{
		"future":             {{At: old, Revision: 10}, {At: now.Add(time.Hour).UnixNano(), Revision: 20}},
		"out of time order":  {{At: old, Revision: 10}, {At: old - 1, Revision: 20}},
		"revision went back": {{At: old - 10, Revision: 50}, {At: old, Revision: 10}},
		"above current":      {{At: old, Revision: 900}},
	} {
		s := &State{Policy: policy, Samples: samples}
		if got := s.Target(now, 100); got != 0 {
			t.Fatalf("%s: target = %d, want 0", name, got)
		}
	}
	good := &State{Policy: policy, Samples: []Sample{{At: old, Revision: 10}}}
	if got := good.Target(now, 100); got != 10 {
		t.Fatalf("usable samples: target = %d, want 10", got)
	}
}

func TestValidScope(t *testing.T) {
	for _, ok := range []string{"", "acme/app/", NodeScope} {
		if !ValidScope(ok) {
			t.Fatalf("ValidScope(%q) = false", ok)
		}
	}
	for _, bad := range []string{"acme/", "acme/app", "acme/app/x/", "/app/", "x"} {
		if ValidScope(bad) {
			t.Fatalf("ValidScope(%q) = true", bad)
		}
	}
}
