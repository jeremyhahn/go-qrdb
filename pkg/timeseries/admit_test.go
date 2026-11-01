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

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// failingGetReader is a storage.Reader whose point reads fail.
type failingGetReader struct {
	storage.Reader
}

func (failingGetReader) Get([]byte) ([]byte, error) { return nil, errors.New("disk gone") }

func storageHolding(t *testing.T, metrics ...string) *Storage {
	t.Helper()
	s := newStorage(t)
	for _, m := range metrics {
		if err := write(t, s, m, 1, ts(2026, 2, 24, 10, 0, 0), nil); err != nil {
			t.Fatalf("write %s: %v", m, err)
		}
	}
	return s
}

func assertMetricLimit(t *testing.T, err error, maxMetrics uint32) {
	t.Helper()
	var le *MetricLimitError
	if !errors.As(err, &le) || le.Max != maxMetrics {
		t.Fatalf("err = %v, want *MetricLimitError{Max: %d}", err, maxMetrics)
	}
}

func TestAdmitMetrics_ZeroCapAdmitsEverything(t *testing.T) {
	s := storageHolding(t, "cpu", "mem")
	if err := AdmitMetrics(s.stg, []string{"disk", "net"}, 0); err != nil {
		t.Fatalf("uncapped AdmitMetrics = %v", err)
	}
}

func TestAdmitMetrics_HeldMetricIsAdmittedAtTheCap(t *testing.T) {
	s := storageHolding(t, "cpu", "mem")
	if err := AdmitMetrics(s.stg, []string{"cpu", "mem", "cpu", ""}, 2); err != nil {
		t.Fatalf("AdmitMetrics of held metrics at the cap = %v", err)
	}
}

func TestAdmitMetrics_FreshMetricIsRefusedAtTheCap(t *testing.T) {
	s := storageHolding(t, "cpu", "mem")
	assertMetricLimit(t, AdmitMetrics(s.stg, []string{"cpu", "disk"}, 2), 2)
}

func TestAdmitMetrics_FreshMetricIsAdmittedUnderTheCap(t *testing.T) {
	s := storageHolding(t, "cpu")
	if err := AdmitMetrics(s.stg, []string{"mem", "mem"}, 2); err != nil {
		t.Fatalf("AdmitMetrics under the cap = %v", err)
	}
}

func TestAdmitMetrics_MoreFreshMetricsThanTheCapAreRefused(t *testing.T) {
	s := storageHolding(t)
	assertMetricLimit(t, AdmitMetrics(s.stg, []string{"a", "b", "c"}, 2), 2)
}

func TestAdmitMetrics_ReadFailureIsAReadError(t *testing.T) {
	s := storageHolding(t)
	err := AdmitMetrics(failingGetReader{Reader: s.stg}, []string{"a"}, 2)
	var re *ReadError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *ReadError", err)
	}
}

func TestMetricLimitError_NamesTheCap(t *testing.T) {
	if got, want := (&MetricLimitError{Max: 3}).Error(), "timeseries: shard holds its limit of 3 metric names"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
