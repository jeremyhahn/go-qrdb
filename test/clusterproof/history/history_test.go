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

//go:build clusterproof

package history

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func sampleHistory() History {
	return History{
		{ID: 1, Client: 0, Kind: KindPut, Key: "k", Input: &Input{Value: "c0-1"}, CallNS: 10, ReturnNS: 20, Outcome: OutcomeOK},
		{ID: 2, Client: 1, Kind: KindGet, Key: "k", Output: &Output{Value: "c0-1", Found: true}, CallNS: 30, ReturnNS: 40, Outcome: OutcomeOK},
		{ID: 3, Client: 1, Kind: KindCAS, Key: "k", Input: &Input{Value: "c1-3", Expected: "c0-1"}, CallNS: 50, ReturnNS: 90, Outcome: OutcomeIndeterminate, Error: "context deadline exceeded"},
		{ID: 4, Client: 0, Kind: KindDelete, Key: "k", CallNS: 60, ReturnNS: 70, Outcome: OutcomeFail, Error: "not leader"},
	}
}

func TestEncodeDecode_RoundTrip(t *testing.T) {
	h := sampleHistory()
	var buf bytes.Buffer
	if err := Encode(&buf, h); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if got := strings.Count(buf.String(), "\n"); got != len(h) {
		t.Fatalf("lines = %d, want %d", got, len(h))
	}
	back, err := Decode(&buf)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(back) != len(h) {
		t.Fatalf("decoded %d ops, want %d", len(back), len(h))
	}
	for i := range h {
		a, b := h[i], back[i]
		if a.ID != b.ID || a.Kind != b.Kind || a.Outcome != b.Outcome || a.CallNS != b.CallNS || a.ReturnNS != b.ReturnNS || a.Error != b.Error {
			t.Errorf("op %d: got %+v, want %+v", i, b, a)
		}
		if (a.Input == nil) != (b.Input == nil) || (a.Input != nil && *a.Input != *b.Input) {
			t.Errorf("op %d input: got %+v, want %+v", i, b.Input, a.Input)
		}
		if (a.Output == nil) != (b.Output == nil) || (a.Output != nil && *a.Output != *b.Output) {
			t.Errorf("op %d output: got %+v, want %+v", i, b.Output, a.Output)
		}
	}
}

func TestEncode_FieldNamesAreTheFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := Encode(&buf, sampleHistory()[:1]); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	line := buf.String()
	for _, field := range []string{`"id":1`, `"client":0`, `"op":"put"`, `"key":"k"`, `"input":{"value":"c0-1"}`, `"call_ns":10`, `"return_ns":20`, `"outcome":"ok"`} {
		if !strings.Contains(line, field) {
			t.Errorf("encoded line %q lacks %s", line, field)
		}
	}
}

func TestDecode_SkipsBlankLines(t *testing.T) {
	in := "\n" + `{"id":1,"client":0,"op":"get","key":"k","call_ns":1,"return_ns":2,"outcome":"fail"}` + "\n\n"
	h, err := Decode(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(h) != 1 {
		t.Fatalf("decoded %d ops, want 1", len(h))
	}
}

func TestDecode_Rejects(t *testing.T) {
	cases := map[string]struct {
		in   string
		line int
	}{
		"malformed json": {in: `{"id":1` + "\n", line: 1},
		"unknown field":  {in: `{"id":1,"client":0,"op":"get","key":"k","call_ns":1,"return_ns":2,"outcome":"ok","extra":true}` + "\n", line: 1},
		"second line":    {in: `{"id":1,"client":0,"op":"get","key":"k","call_ns":1,"return_ns":2,"outcome":"ok"}` + "\nnot json\n", line: 2},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Decode(strings.NewReader(tc.in))
			var de *DecodeError
			if !errors.As(err, &de) {
				t.Fatalf("err = %v, want *DecodeError", err)
			}
			if de.Line != tc.line {
				t.Errorf("line = %d, want %d", de.Line, tc.line)
			}
			if !strings.Contains(de.Error(), "history: line") || de.Unwrap() == nil {
				t.Errorf("error %q does not name its line or wrap its cause", de.Error())
			}
		})
	}
}

func TestDecode_ValidatesRecords(t *testing.T) {
	in := `{"id":1,"client":0,"op":"put","key":"k","call_ns":1,"return_ns":2,"outcome":"ok"}` + "\n"
	_, err := Decode(strings.NewReader(in))
	var ie *InvalidOpError
	if !errors.As(err, &ie) || ie.ID != 1 {
		t.Fatalf("err = %v, want *InvalidOpError for op 1", err)
	}
}

func TestDecode_OverlongLineIsAnError(t *testing.T) {
	in := `{"id":1,"client":0,"op":"get","key":"` + strings.Repeat("x", maxLineBytes) + `"}` + "\n"
	_, err := Decode(strings.NewReader(in))
	var de *DecodeError
	if !errors.As(err, &de) || de.Line != 0 {
		t.Fatalf("err = %v, want reader-level *DecodeError", err)
	}
	if !strings.Contains(de.Error(), "history: read:") {
		t.Errorf("error %q", de.Error())
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestEncode_WriterFailure(t *testing.T) {
	err := Encode(failWriter{}, sampleHistory())
	var ee *EncodeError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v, want *EncodeError", err)
	}
	if !strings.Contains(ee.Error(), "disk full") || ee.Unwrap() == nil {
		t.Errorf("error %q does not carry its cause", ee.Error())
	}
}

func TestValidate(t *testing.T) {
	ok := Op{ID: 1, Kind: KindGet, Key: "k", CallNS: 1, ReturnNS: 1, Outcome: OutcomeOK, Output: &Output{}}
	cases := map[string]struct {
		mutate func(*Op)
		reason string
	}{
		"unknown op":       {func(o *Op) { o.Kind = "append" }, "unknown op"},
		"unknown outcome":  {func(o *Op) { o.Outcome = "maybe" }, "unknown outcome"},
		"empty key":        {func(o *Op) { o.Key = "" }, "empty key"},
		"time reversed":    {func(o *Op) { o.ReturnNS = 0 }, "precedes"},
		"output on fail":   {func(o *Op) { o.Outcome = OutcomeFail; o.Output = &Output{} }, "output"},
		"put without in":   {func(o *Op) { o.Kind = KindPut }, "without input"},
		"cas without in":   {func(o *Op) { o.Kind = KindCAS }, "without input"},
		"output on indet.": {func(o *Op) { o.Outcome = OutcomeIndeterminate; o.Output = &Output{} }, "output"},
		"ok get no output": {func(o *Op) { o.Output = nil }, "without output"},
	}
	if err := (History{ok}).Validate(); err != nil {
		t.Fatalf("valid op rejected: %v", err)
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			op := ok
			tc.mutate(&op)
			err := History{op}.Validate()
			var ie *InvalidOpError
			if !errors.As(err, &ie) || !strings.Contains(ie.Error(), tc.reason) {
				t.Fatalf("err = %v, want InvalidOpError mentioning %q", err, tc.reason)
			}
		})
	}
	t.Run("duplicate id", func(t *testing.T) {
		err := History{ok, ok}.Validate()
		var ie *InvalidOpError
		if !errors.As(err, &ie) || !strings.Contains(ie.Error(), "duplicate") {
			t.Fatalf("err = %v, want duplicate-id InvalidOpError", err)
		}
	})
}

func TestOp_WritesAndWritten(t *testing.T) {
	cases := map[string]struct {
		op        Op
		writes    bool
		value     string
		hasValue  bool
		ofDeleted bool
	}{
		"put ok":            {Op{Kind: KindPut, Input: &Input{Value: "v"}, Outcome: OutcomeOK}, true, "v", true, false},
		"put indeterminate": {Op{Kind: KindPut, Input: &Input{Value: "v"}, Outcome: OutcomeIndeterminate}, true, "v", true, false},
		"put fail":          {Op{Kind: KindPut, Input: &Input{Value: "v"}, Outcome: OutcomeFail}, false, "v", true, false},
		"delete ok":         {Op{Kind: KindDelete, Outcome: OutcomeOK}, true, "", false, true},
		"delete fail":       {Op{Kind: KindDelete, Outcome: OutcomeFail}, false, "", false, true},
		"cas swapped":       {Op{Kind: KindCAS, Input: &Input{Value: "n"}, Output: &Output{Swapped: true}, Outcome: OutcomeOK}, true, "n", true, false},
		"cas not swapped":   {Op{Kind: KindCAS, Input: &Input{Value: "n"}, Output: &Output{}, Outcome: OutcomeOK}, false, "n", true, false},
		"cas indeterminate": {Op{Kind: KindCAS, Input: &Input{Value: "n"}, Outcome: OutcomeIndeterminate}, true, "n", true, false},
		"cas fail":          {Op{Kind: KindCAS, Input: &Input{Value: "n"}, Outcome: OutcomeFail}, false, "n", true, false},
		"get":               {Op{Kind: KindGet, Output: &Output{Value: "v", Found: true}, Outcome: OutcomeOK}, false, "", false, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.op.Writes(); got != tc.writes {
				t.Errorf("Writes() = %v, want %v", got, tc.writes)
			}
			v, ok := tc.op.Written()
			if v != tc.value || ok != tc.hasValue {
				t.Errorf("Written() = (%q, %v), want (%q, %v)", v, ok, tc.value, tc.hasValue)
			}
		})
	}
}

func TestSort_ByCallThenID(t *testing.T) {
	h := History{{ID: 3, CallNS: 5}, {ID: 2, CallNS: 5}, {ID: 1, CallNS: 9}}
	h.Sort()
	if h[0].ID != 2 || h[1].ID != 3 || h[2].ID != 1 {
		t.Fatalf("order = %d,%d,%d, want 2,3,1", h[0].ID, h[1].ID, h[2].ID)
	}
}

// stepClock advances one nanosecond per reading, so every call and return in
// a test gets a distinct, predictable time.
type stepClock struct{ n atomic.Int64 }

func (c *stepClock) Now() int64 { return c.n.Add(1) }

func TestRecorder_StampsAndKeepsOutcome(t *testing.T) {
	clk := &stepClock{}
	r := NewRecorder(clk)
	if r.Clock() != clk {
		t.Fatal("Clock() is not the clock the recorder was built with")
	}
	l := r.Log(7)
	p := l.Call(KindGet, "k", nil)
	if p.ID() != 1 {
		t.Fatalf("first ID = %d, want 1", p.ID())
	}
	op := l.Return(p, OutcomeOK, &Output{Value: "v", Found: true}, nil)
	if op.Client != 7 || op.CallNS != 1 || op.ReturnNS != 2 || op.Output == nil || op.Error != "" {
		t.Fatalf("recorded %+v", op)
	}

	p = l.Call(KindPut, "k", &Input{Value: "x"})
	op = l.Return(p, OutcomeIndeterminate, &Output{Value: "ignored"}, errors.New("timeout"))
	if op.Output != nil {
		t.Errorf("output kept on an indeterminate op: %+v", op.Output)
	}
	if op.Error != "timeout" {
		t.Errorf("error = %q, want timeout", op.Error)
	}
	if err := r.History().Validate(); err != nil {
		t.Fatalf("recorded history invalid: %v", err)
	}
}

func TestRecorder_ConcurrentClientsMergeSorted(t *testing.T) {
	r := NewRecorder(NewMonotonicClock())
	const clients, per = 8, 200
	var wg sync.WaitGroup
	for c := range clients {
		l := r.Log(c)
		wg.Go(func() {
			for range per {
				p := l.Call(KindGet, "k", nil)
				l.Return(p, OutcomeFail, nil, nil)
			}
		})
	}
	wg.Wait()
	h := r.History()
	if len(h) != clients*per {
		t.Fatalf("history has %d ops, want %d", len(h), clients*per)
	}
	if err := h.Validate(); err != nil {
		t.Fatalf("merged history invalid: %v", err)
	}
	for i := 1; i < len(h); i++ {
		if h[i].CallNS < h[i-1].CallNS {
			t.Fatalf("history not sorted at %d", i)
		}
	}
}

func TestMonotonicClock_NeverGoesBackwards(t *testing.T) {
	c := NewMonotonicClock()
	prev := c.Now()
	for range 1000 {
		now := c.Now()
		if now < prev {
			t.Fatalf("clock went backwards: %d then %d", prev, now)
		}
		prev = now
	}
}
