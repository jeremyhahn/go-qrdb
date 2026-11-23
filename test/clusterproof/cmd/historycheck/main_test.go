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

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
)

// writeHistory encodes h as JSON lines into a file under t.TempDir and
// returns its path.
func writeHistory(t *testing.T, h history.History) string {
	t.Helper()
	var buf bytes.Buffer
	if err := history.Encode(&buf, h); err != nil {
		t.Fatalf("encode: %v", err)
	}
	path := filepath.Join(t.TempDir(), "history.jsonl")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func put(id uint64, client int, key, value string, call, ret int64, outcome history.Outcome) history.Op {
	return history.Op{ID: id, Client: client, Kind: history.KindPut, Key: key,
		Input: &history.Input{Value: value}, CallNS: call, ReturnNS: ret, Outcome: outcome}
}

func get(id uint64, client int, key, value string, found bool, call, ret int64) history.Op {
	return history.Op{ID: id, Client: client, Kind: history.KindGet, Key: key,
		Output: &history.Output{Value: value, Found: found}, CallNS: call, ReturnNS: ret, Outcome: history.OutcomeOK}
}

func runOn(t *testing.T, path string) (int, history.Result, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run([]string{path}, &stdout, &stderr)
	var res history.Result
	if code != exitError {
		if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
			t.Fatalf("stdout is not a history.Result: %v\n%s", err, stdout.String())
		}
	}
	return code, res, stderr.String()
}

// TestRun_LinearizableHistory covers a history with an indeterminate put, a
// failed get and reads that follow the writes: it is linearizable, so the
// command exits 0 and prints Valid.
func TestRun_LinearizableHistory(t *testing.T) {
	h := history.History{
		put(1, 0, "k", "v1", 10, 20, history.OutcomeOK),
		get(2, 1, "k", "v1", true, 30, 40),
		put(3, 0, "k", "v2", 50, 60, history.OutcomeOK),
		{ID: 4, Client: 1, Kind: history.KindGet, Key: "k", CallNS: 65, ReturnNS: 66, Outcome: history.OutcomeFail, Error: "node down"},
		get(5, 1, "k", "v2", true, 70, 80),
		put(6, 0, "j", "w1", 90, 100, history.OutcomeIndeterminate),
		get(7, 1, "absent", "", false, 110, 120),
	}
	code, res, stderr := runOn(t, writeHistory(t, h))
	if code != exitValid || !res.Valid {
		t.Fatalf("exit %d, valid %v, stderr %q; want 0 and valid", code, res.Valid, stderr)
	}
}

// TestRun_StaleReadHistory covers a read that returns the value a completed
// later write replaced: the command exits 1 and prints an invalid Result.
func TestRun_StaleReadHistory(t *testing.T) {
	h := history.History{
		put(1, 0, "k", "v1", 10, 20, history.OutcomeOK),
		put(2, 0, "k", "v2", 30, 40, history.OutcomeOK),
		get(3, 1, "k", "v1", true, 50, 60),
	}
	code, res, stderr := runOn(t, writeHistory(t, h))
	if code != exitAnomalies || res.Valid {
		t.Fatalf("exit %d, valid %v, stderr %q; want 1 and invalid", code, res.Valid, stderr)
	}
}

// TestRun_Errors covers the usage error and an unreadable or invalid history:
// each exits 2.
func TestRun_Errors(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.jsonl")
	if err := os.WriteFile(bad, []byte("{not json\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	for name, args := range map[string][]string{
		"no args":   nil,
		"two args":  {"a", "b"},
		"missing":   {filepath.Join(t.TempDir(), "missing.jsonl")},
		"malformed": {bad},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != exitError {
			t.Errorf("%s: exit %d, want %d", name, code, exitError)
		}
		if stderr.Len() == 0 {
			t.Errorf("%s: no error on stderr", name)
		}
	}
}
