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

package protocol

import (
	"errors"
	"testing"
)

func TestLimitRefusal_RoundTrips(t *testing.T) {
	for _, resource := range []LimitResource{LimitIndexes, LimitMetrics} {
		data := EncodeLimitRefusal(resource, 42)
		got, maxCount, ok := DecodeLimitRefusal(data)
		if !ok || got != resource || maxCount != 42 {
			t.Fatalf("DecodeLimitRefusal = (%d, %d, %v), want (%d, 42, true)", got, maxCount, ok, resource)
		}
	}
}

func TestLimitRefusal_DecodeRejectsOtherData(t *testing.T) {
	full := EncodeLimitRefusal(LimitIndexes, 3)
	for name, data := range map[string][]byte{
		"empty":       nil,
		"plain error": EncodeAppError(AppErrorInvalid, "boom"),
		"truncated":   full[:len(full)-1],
		"extended":    append(append([]byte{}, full...), 0),
	} {
		if _, _, ok := DecodeLimitRefusal(data); ok {
			t.Fatalf("%s: decoded as a limit refusal", name)
		}
	}
}

func TestLimitRefusal_GenericReaderGetsAConflict(t *testing.T) {
	msg, kind, ok := DecodeAppErrorKind(EncodeLimitRefusal(LimitMetrics, 5))
	if !ok || kind != AppErrorConflict || msg != "shard holds its limit of 5 metric names" {
		t.Fatalf("DecodeAppErrorKind = (%q, %v, %v)", msg, kind, ok)
	}
	msg, _, _ = DecodeAppErrorKind(EncodeLimitRefusal(LimitResource(99), 1))
	if msg != "shard holds its limit of 1 resource 99" {
		t.Fatalf("unknown resource message = %q", msg)
	}
}

func TestIndexCreateOpts_CarriesTheIndexCap(t *testing.T) {
	for _, maxIndexes := range []uint32{0, 1, 1 << 31} {
		cmd, err := EncodeIndexCreateOpts("idx", "f", IndexCreateOptions{Owner: "o", MaxIndexes: maxIndexes})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		_, _, opts, err := DecodeIndexCreateOpts(cmd[1:])
		if err != nil || opts.MaxIndexes != maxIndexes || opts.Owner != "o" {
			t.Fatalf("decode = (%+v, %v), want MaxIndexes %d", opts, err, maxIndexes)
		}
		uncapped, _ := EncodeIndexCreateOpts("idx", "f", IndexCreateOptions{Owner: "o"})
		if want := len(uncapped) + map[bool]int{true: 4}[maxIndexes > 0]; len(cmd) != want {
			t.Fatalf("encoded length = %d, want %d", len(cmd), want)
		}
	}
}

func TestIndexCreateOpts_PayloadEndingAtTheOwnerHasNoCap(t *testing.T) {
	cmd, err := EncodeIndexCreateOpts("idx", "f", IndexCreateOptions{MaxIndexes: 9})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	_, _, opts, err := DecodeIndexCreateOpts(cmd[1 : len(cmd)-4])
	if err != nil || opts.MaxIndexes != 0 {
		t.Fatalf("decode = (%+v, %v), want no cap", opts, err)
	}
}

func TestIndexCreateOpts_MalformedCapIsRefused(t *testing.T) {
	cmd, err := EncodeIndexCreateOpts("idx", "f", IndexCreateOptions{MaxIndexes: 9})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var ice *InvalidCommandError
	if _, _, _, err := DecodeIndexCreateOpts(cmd[1 : len(cmd)-1]); !errors.As(err, &ice) {
		t.Fatalf("a three-byte cap decoded: %v", err)
	}
}

func TestTSWrite_CarriesTheMetricCap(t *testing.T) {
	cmd, err := EncodeTSWrite("cpu", 1, 2, map[string]string{"h": "a"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if got := AppendTSMetricCap(cmd, 0); len(got) != len(cmd) {
		t.Fatal("a zero cap extended the command")
	}
	p, err := DecodeTSWrite(AppendTSMetricCap(cmd, 12)[1:])
	if err != nil || p.MaxMetrics != 12 || p.Metric != "cpu" || p.Tags["h"] != "a" {
		t.Fatalf("decode = (%+v, %v), want MaxMetrics 12", p, err)
	}
	p, err = DecodeTSWrite(cmd[1:])
	if err != nil || p.MaxMetrics != 0 {
		t.Fatalf("uncapped decode = (%+v, %v)", p, err)
	}
}

func TestTSWrite_MalformedMetricCapIsRefused(t *testing.T) {
	cmd, err := EncodeTSWrite("cpu", 1, 2, nil)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var ice *InvalidCommandError
	if _, err := DecodeTSWrite(append(cmd[1:], 1, 2)); !errors.As(err, &ice) {
		t.Fatalf("a two-byte cap decoded: %v", err)
	}
}

func TestTSWriteBatch_CarriesTheMetricCap(t *testing.T) {
	cmd, err := EncodeTSWriteBatch([]TSWriteParams{{Metric: "a", Value: 1}, {Metric: "b", Value: 2}})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	points, maxMetrics, err := DecodeTSWriteBatch(AppendTSMetricCap(cmd, 3)[1:])
	if err != nil || maxMetrics != 3 || len(points) != 2 || points[1].Metric != "b" {
		t.Fatalf("decode = (%v, %d, %v), want two points capped at 3", points, maxMetrics, err)
	}
	var ice *InvalidCommandError
	if _, _, err := DecodeTSWriteBatch(append(cmd[1:], 7)); !errors.As(err, &ice) {
		t.Fatalf("a one-byte cap decoded: %v", err)
	}
}
