//go:build !qrdb_ts

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
	"context"
	"errors"
	"testing"
	"time"
)

// TestStubErrNotCompiledMessage verifies the disabled-feature sentinel keeps
// the established "build with -tags" message shape.
func TestStubErrNotCompiledMessage(t *testing.T) {
	want := "timeseries support not compiled: build with -tags qrdb_ts"
	if got := ErrNotCompiled.Error(); got != want {
		t.Errorf("ErrNotCompiled.Error() = %q, want %q", got, want)
	}
}

// TestStubNew verifies the stub constructor validates its input and returns
// a usable handle for a non-nil database.
func TestStubNew(t *testing.T) {
	var nilDBErr *NilDBError
	if s, err := New(nil); !errors.As(err, &nilDBErr) || s != nil {
		t.Errorf("New(nil) = (%v, %v), want (nil, *NilDBError)", s, err)
	}
}

// TestStubNewWithDir verifies the stub directory constructor reports that
// time-series support is not compiled in.
func TestStubNewWithDir(t *testing.T) {
	if s, err := NewWithDir("/tmp/never-created"); !errors.Is(err, ErrNotCompiled) || s != nil {
		t.Errorf("NewWithDir = (%v, %v), want (nil, ErrNotCompiled)", s, err)
	}
}

// TestStubStorageOperations verifies every storage operation reports that
// time-series support is not compiled in.
func TestStubStorageOperations(t *testing.T) {
	s := &Storage{}
	ctx := context.Background()
	now := time.Now()

	tests := []struct {
		name string
		op   func() error
	}{
		{"Write", func() error { return s.Write("cpu", 1.0, now, nil) }},
		{"WritePoint", func() error {
			return s.WritePoint(ctx, &DataPoint{Metric: "cpu", Timestamp: now})
		}},
		{"WriteBatch", func() error {
			return s.WriteBatch([]DataPoint{{Metric: "cpu", Timestamp: now}})
		}},
		{"Query", func() error {
			_, err := s.Query("cpu", now.Add(-time.Hour), now, WithTSLimit(1))
			return err
		}},
		{"QueryRich", func() error {
			_, err := s.QueryRich(ctx, &QueryRequest{Metric: "cpu", StartTime: now.Add(-time.Hour), EndTime: now})
			return err
		}},
		{"DeleteSeries", func() error {
			return s.DeleteSeries(ctx, &DeleteRequest{Metric: "cpu"})
		}},
		{"GetRetentionPolicy", func() error {
			_, err := s.GetRetentionPolicy("cpu")
			return err
		}},
		{"SetRetentionPolicy", func() error {
			return s.SetRetentionPolicy("cpu", &RetentionPolicy{Duration: time.Hour})
		}},
		{"ListMetrics", func() error {
			_, err := s.ListMetrics("", 0)
			return err
		}},
		{"Aggregate", func() error {
			_, err := s.Aggregate("cpu", now.Add(-time.Hour), now, AggAvg, time.Minute)
			return err
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.op(); !errors.Is(err, ErrNotCompiled) {
				t.Errorf("%s error = %v, want ErrNotCompiled", tc.name, err)
			}
		})
	}
}

// TestStubStorageZeroState verifies the stub storage reports empty state and
// closes cleanly.
func TestStubStorageZeroState(t *testing.T) {
	s := &Storage{}
	if db := s.DB(); db != nil {
		t.Errorf("DB() = %v, want nil", db)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close() error = %v, want nil", err)
	}
}

// TestStubStorageOptions verifies the stub option constructors return
// applicable no-op options.
func TestStubStorageOptions(t *testing.T) {
	if opt := WithBarrier(nil); opt == nil {
		t.Error("WithBarrier(nil) returned nil option")
	}
	if opt := WithEncryptor(nil); opt == nil {
		t.Error("WithEncryptor(nil) returned nil option")
	}
}

// TestStubDataTypeValidation verifies the stub retains real validation
// behavior on the pure data types shared with untagged wire handlers.
func TestStubDataTypeValidation(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		err     error
		wantMsg string
	}{
		{
			name:    "valid data point",
			err:     (&DataPoint{Metric: "cpu", Timestamp: now}).Validate(),
			wantMsg: "",
		},
		{
			name:    "empty metric",
			err:     (&DataPoint{Timestamp: now}).Validate(),
			wantMsg: "timeseries: metric name must not be empty",
		},
		{
			name:    "zero timestamp",
			err:     (&DataPoint{Metric: "cpu"}).Validate(),
			wantMsg: "timeseries: timestamp must not be zero",
		},
		{
			name:    "empty write request",
			err:     (&WriteRequest{}).Validate(),
			wantMsg: "timeseries: WriteRequest.Validate: no data points to write",
		},
		{
			name: "inverted query range",
			err: (&QueryRequest{
				Metric:    "cpu",
				StartTime: now,
				EndTime:   now.Add(-time.Hour),
			}).Validate(),
			wantMsg: (&InvalidTimeRangeError{Start: now, End: now.Add(-time.Hour)}).Error(),
		},
		{
			name:    "delete request without metric",
			err:     (&DeleteRequest{}).Validate(),
			wantMsg: "timeseries: metric name must not be empty",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantMsg == "" {
				if tc.err != nil {
					t.Errorf("Validate() error = %v, want nil", tc.err)
				}
				return
			}
			if tc.err == nil || tc.err.Error() != tc.wantMsg {
				t.Errorf("Validate() error = %v, want %q", tc.err, tc.wantMsg)
			}
		})
	}
}

// TestStubAggFunc verifies the AggFunc helpers behave identically to the
// real package.
func TestStubAggFunc(t *testing.T) {
	tests := []struct {
		fn    AggFunc
		valid bool
		name  string
	}{
		{AggNone, false, "none"},
		{AggAvg, true, "avg"},
		{AggSum, true, "sum"},
		{AggMin, true, "min"},
		{AggMax, true, "max"},
		{AggCount, true, "count"},
		{AggPercentile, true, "percentile"},
		{AggFirst, true, "first"},
		{AggLast, true, "last"},
		{AggFunc(99), false, "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.fn.IsValid(); got != tc.valid {
				t.Errorf("IsValid() = %v, want %v", got, tc.valid)
			}
			if got := tc.fn.String(); got != tc.name {
				t.Errorf("String() = %q, want %q", got, tc.name)
			}
		})
	}
}

// TestStubQueryOptions verifies the query option constructors resolve into
// the expected TSQueryOptions.
func TestStubQueryOptions(t *testing.T) {
	tags := map[string]string{"host": "a"}
	got := ApplyTSQueryOptions([]TSQueryOption{WithTags(tags), WithTSLimit(7), WithDescending()})
	if got.Tags["host"] != "a" || got.Limit != 7 || !got.Descending {
		t.Errorf("ApplyTSQueryOptions = %+v, want tags host=a, limit 7, descending", got)
	}
}
