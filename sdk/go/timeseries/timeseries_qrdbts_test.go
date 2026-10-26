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

//go:build qrdb_ts

package timeseries_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	sdkts "github.com/jeremyhahn/go-qrdb/sdk/go/timeseries"
)

// markerMetric is a metric name no store file holds unless it was written
// in the clear.
const markerMetric = "sdk_ts_plaintext_marker_4e8b2d71c9a0"

// pointTime is the timestamp every point in these tests is written at.
var pointTime = time.Unix(1_700_000_000, 0).UTC()

// open opens a directory-backed store, with enc when it is non-nil.
func open(t *testing.T, dir string, enc *atrest.Encryptor) *sdkts.TimeSeriesDirStorage {
	t.Helper()
	ts, err := sdkts.NewTimeSeriesStorageWithEncryptor(dir, enc)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	return ts
}

// writePoint stages one point into a batch over ts and commits it.
func writePoint(t *testing.T, ts *sdkts.TimeSeriesDirStorage, value float64) {
	t.Helper()
	b := ts.Batch()
	defer func() { _ = b.Close() }()
	if err := ts.Write(b, markerMetric, value, pointTime, map[string]string{"host": "a"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

// queryValue reads back the single point written at pointTime.
func queryValue(t *testing.T, ts *sdkts.TimeSeriesDirStorage) float64 {
	t.Helper()
	got, err := ts.Query(markerMetric, pointTime, pointTime)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 || got[0].Metric != markerMetric || got[0].Tags["host"] != "a" {
		t.Fatalf("Query = %+v, want one %s point tagged host=a", got, markerMetric)
	}
	return got[0].Value
}

// filesContain reports whether any regular file under dir holds needle.
func filesContain(t *testing.T, dir string, needle []byte) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || found {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		found = bytes.Contains(data, needle)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return found
}

// TestNewTimeSeriesStorageWithDir_WriteQueryPersists checks a point written
// through Batch is read back, and survives Close and a reopen of the
// directory.
func TestNewTimeSeriesStorageWithDir_WriteQueryPersists(t *testing.T) {
	dir := t.TempDir()
	ts, err := sdkts.NewTimeSeriesStorageWithDir(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	writePoint(t, ts, 42.5)
	if v := queryValue(t, ts); v != 42.5 {
		t.Fatalf("value = %v, want 42.5", v)
	}
	if err := ts.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ts = open(t, dir, nil)
	defer func() { _ = ts.Close() }()
	if v := queryValue(t, ts); v != 42.5 {
		t.Fatalf("value after reopen = %v, want 42.5", v)
	}
	if !filesContain(t, dir, []byte(markerMetric)) {
		t.Fatal("a plaintext store holds no clear copy of the metric: the encrypted case below proves nothing")
	}
}

// TestNewTimeSeriesStorageWithEncryptor_EncryptsAtRest checks an encrypted
// store reads its point back across a reopen under the same encryptor, and
// that no file on disk holds the metric name in the clear.
func TestNewTimeSeriesStorageWithEncryptor_EncryptsAtRest(t *testing.T) {
	dir := t.TempDir()
	enc := testEncryptor(t)
	ts := open(t, dir, enc)
	writePoint(t, ts, 7)
	if err := ts.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ts = open(t, dir, enc)
	defer func() { _ = ts.Close() }()
	if v := queryValue(t, ts); v != 7 {
		t.Fatalf("value after reopen = %v, want 7", v)
	}
	if filesContain(t, dir, []byte(markerMetric)) {
		t.Fatal("an encrypted store holds the metric name in the clear")
	}
}
