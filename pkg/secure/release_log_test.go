// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package secure

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-enclave"
)

// captureLog routes the default slog logger into a buffer for the rest of the
// test and returns the buffer.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &out
}

// logRecords decodes every JSON record in out.
func logRecords(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()
	var recs []map[string]any
	dec := json.NewDecoder(out)
	for dec.More() {
		var rec map[string]any
		if err := dec.Decode(&rec); err != nil {
			t.Fatalf("decode log record: %v", err)
		}
		recs = append(recs, rec)
	}
	return recs
}

func TestLogRelease_NilIsSilent(t *testing.T) {
	out := captureLog(t)
	LogRelease("buffer", nil)
	if recs := logRecords(t, out); len(recs) != 0 {
		t.Fatalf("LogRelease(nil) logged %v, want nothing", recs)
	}
}

func TestLogRelease_Levels(t *testing.T) {
	for name, tc := range map[string]struct {
		err   error
		level string
	}{
		"released without wipe": {enclave.ErrReleasedWithoutWipe, "WARN"},
		"release refused":       {enclave.ErrLockedMemoryUnavailable, "WARN"},
		"corrupted":             {errors.Join(enclave.ErrEnclaveCorrupted, enclave.ErrLockedMemoryUnavailable), "ERROR"},
	} {
		t.Run(name, func(t *testing.T) {
			out := captureLog(t)
			LogRelease("buffer", tc.err)
			recs := logRecords(t, out)
			if len(recs) != 1 {
				t.Fatalf("logged %d records, want 1", len(recs))
			}
			if got := recs[0]["level"]; got != tc.level {
				t.Errorf("level = %v, want %s", got, tc.level)
			}
			if got := recs[0]["what"]; got != "buffer" {
				t.Errorf("what = %v, want buffer", got)
			}
			if got := recs[0]["error"]; got != tc.err.Error() {
				t.Errorf("error = %v, want %q", got, tc.err.Error())
			}
		})
	}
}
