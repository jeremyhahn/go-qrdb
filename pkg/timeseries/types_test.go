//go:build qrdb_ts

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

package timeseries

import (
	"errors"
	"testing"
	"time"
)

func TestDataPoint_Validate_ZeroTimestamp(t *testing.T) {
	dp := &DataPoint{
		Metric:    "cpu.usage",
		Value:     42.5,
		Timestamp: time.Time{},
	}
	err := dp.Validate()
	if err == nil {
		t.Fatal("Validate(zero timestamp): expected error, got nil")
	}
	var ite *InvalidTimestampError
	if !errors.As(err, &ite) {
		t.Fatalf("Validate(zero timestamp): expected InvalidTimestampError, got %T: %v", err, err)
	}
}

func TestAggFunc_IsValid_KnownFuncs(t *testing.T) {
	validFuncs := []AggFunc{AggAvg, AggSum, AggMin, AggMax, AggCount, AggPercentile, AggFirst, AggLast}
	for _, f := range validFuncs {
		if !f.IsValid() {
			t.Errorf("IsValid(%d): expected true", f)
		}
	}
}

func TestAggFunc_IsValid_InvalidFuncs(t *testing.T) {
	invalidFuncs := []AggFunc{AggNone, AggFunc(99), AggFunc(255)}
	for _, f := range invalidFuncs {
		if f.IsValid() {
			t.Errorf("IsValid(%d): expected false", f)
		}
	}
}

func TestAggFunc_String(t *testing.T) {
	tests := []struct {
		fn   AggFunc
		want string
	}{
		{AggNone, "none"},
		{AggAvg, "avg"},
		{AggSum, "sum"},
		{AggMin, "min"},
		{AggMax, "max"},
		{AggCount, "count"},
		{AggPercentile, "percentile"},
		{AggFirst, "first"},
		{AggLast, "last"},
		{AggFunc(99), "unknown"},
	}
	for _, tt := range tests {
		got := tt.fn.String()
		if got != tt.want {
			t.Errorf("String(%d): got %q, want %q", tt.fn, got, tt.want)
		}
	}
}

func TestQueryRequest_Validate_ZeroStartTime(t *testing.T) {
	qr := &QueryRequest{
		Metric:    "cpu.usage",
		StartTime: time.Time{},
		EndTime:   time.Now(),
	}
	err := qr.Validate()
	if err == nil {
		t.Fatal("Validate(zero start): expected error, got nil")
	}
	var ite *InvalidTimestampError
	if !errors.As(err, &ite) {
		t.Fatalf("Validate(zero start): expected InvalidTimestampError, got %T: %v", err, err)
	}
}

func TestQueryRequest_Validate_ZeroEndTime(t *testing.T) {
	qr := &QueryRequest{
		Metric:    "cpu.usage",
		StartTime: time.Now(),
		EndTime:   time.Time{},
	}
	err := qr.Validate()
	if err == nil {
		t.Fatal("Validate(zero end): expected error, got nil")
	}
	var ite *InvalidTimestampError
	if !errors.As(err, &ite) {
		t.Fatalf("Validate(zero end): expected InvalidTimestampError, got %T: %v", err, err)
	}
}

func TestQueryRequest_Validate_EndBeforeStart(t *testing.T) {
	qr := &QueryRequest{
		Metric:    "cpu.usage",
		StartTime: ts(2026, 3, 1, 12, 0, 0),
		EndTime:   ts(2026, 3, 1, 10, 0, 0),
	}
	err := qr.Validate()
	if err == nil {
		t.Fatal("Validate(end before start): expected error, got nil")
	}
	var itr *InvalidTimeRangeError
	if !errors.As(err, &itr) {
		t.Fatalf("Validate(end before start): expected InvalidTimeRangeError, got %T: %v", err, err)
	}
}

func TestQueryRequest_Validate_InvalidAggFunc(t *testing.T) {
	qr := &QueryRequest{
		Metric:      "cpu.usage",
		StartTime:   ts(2026, 3, 1, 10, 0, 0),
		EndTime:     ts(2026, 3, 1, 11, 0, 0),
		Aggregation: AggFunc(99),
	}
	err := qr.Validate()
	if err == nil {
		t.Fatal("Validate(invalid agg): expected error, got nil")
	}
	var iaf *InvalidAggFuncError
	if !errors.As(err, &iaf) {
		t.Fatalf("Validate(invalid agg): expected InvalidAggFuncError, got %T: %v", err, err)
	}
}

func TestQueryRequest_Validate_PercentileOutOfRange(t *testing.T) {
	for _, p := range []float64{-5.0, 150.0} {
		qr := &QueryRequest{
			Metric:      "cpu.usage",
			StartTime:   ts(2026, 3, 1, 10, 0, 0),
			EndTime:     ts(2026, 3, 1, 11, 0, 0),
			Aggregation: AggPercentile,
			Percentile:  p,
		}
		err := qr.Validate()
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("Validate(percentile=%v): expected ValidationError, got %T: %v", p, err, err)
		}
	}
}

func TestWriteRequest_Validate_Valid(t *testing.T) {
	wr := &WriteRequest{
		Points: []*DataPoint{
			{Metric: "cpu.usage", Value: 1.0, Timestamp: time.Now()},
			{Metric: "mem.usage", Value: 2.0, Timestamp: time.Now()},
		},
	}
	if err := wr.Validate(); err != nil {
		t.Fatalf("Validate(valid): unexpected error: %v", err)
	}
}

func TestWriteRequest_Validate_EmptyPoints(t *testing.T) {
	wr := &WriteRequest{Points: []*DataPoint{}}
	err := wr.Validate()
	if err == nil {
		t.Fatal("Validate(empty): expected error, got nil")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate(empty): expected ValidationError, got %T: %v", err, err)
	}
}

func TestWriteRequest_Validate_InvalidPointInBatch(t *testing.T) {
	wr := &WriteRequest{
		Points: []*DataPoint{
			{Metric: "cpu.usage", Value: 1.0, Timestamp: time.Now()},
			{Metric: "", Value: 2.0, Timestamp: time.Now()}, // invalid
		},
	}
	err := wr.Validate()
	if err == nil {
		t.Fatal("Validate(invalid point): expected error, got nil")
	}
	var ibpe *InvalidBatchPointError
	if !errors.As(err, &ibpe) {
		t.Fatalf("Validate(invalid point): expected InvalidBatchPointError, got %T: %v", err, err)
	}
	if ibpe.Index != 1 {
		t.Errorf("Validate(invalid point): index got %d, want 1", ibpe.Index)
	}
}

func TestDeleteRequest_Validate_EndBeforeStart(t *testing.T) {
	dr := &DeleteRequest{
		Metric:    "cpu.usage",
		StartTime: ts(2026, 3, 1, 12, 0, 0),
		EndTime:   ts(2026, 3, 1, 10, 0, 0),
	}
	err := dr.Validate()
	if err == nil {
		t.Fatal("Validate(end before start): expected error, got nil")
	}
	var itr *InvalidTimeRangeError
	if !errors.As(err, &itr) {
		t.Fatalf("Validate(end before start): expected InvalidTimeRangeError, got %T: %v", err, err)
	}
}
