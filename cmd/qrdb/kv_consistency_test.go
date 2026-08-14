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

//go:build !integration

// kv_consistency_test.go covers the --consistency / --write-mode KV flag
// option builders and their error paths.
package main

import (
	"strings"
	"testing"
)

func TestReadConsistencyOpts(t *testing.T) {
	t.Run("empty yields no option", func(t *testing.T) {
		opts, err := readConsistencyOpts("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if opts != nil {
			t.Fatalf("expected nil opts for empty level, got %d", len(opts))
		}
	})
	for _, lvl := range []string{"strong", "linearizable", "eventual", "stale"} {
		t.Run("valid "+lvl, func(t *testing.T) {
			opts, err := readConsistencyOpts(lvl)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", lvl, err)
			}
			if len(opts) != 1 {
				t.Fatalf("expected 1 option for %q, got %d", lvl, len(opts))
			}
		})
	}
	t.Run("invalid errors", func(t *testing.T) {
		if _, err := readConsistencyOpts("bogus"); err == nil {
			t.Fatal("expected error for invalid consistency level")
		}
	})
}

func TestWriteModeOpts(t *testing.T) {
	t.Run("empty yields no option", func(t *testing.T) {
		opts, err := writeModeOpts("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if opts != nil {
			t.Fatalf("expected nil opts for empty mode, got %d", len(opts))
		}
	})
	for _, m := range []string{"sync", "wal_durable", "batched"} {
		t.Run("valid "+m, func(t *testing.T) {
			opts, err := writeModeOpts(m)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", m, err)
			}
			if len(opts) != 1 {
				t.Fatalf("expected 1 option for %q, got %d", m, len(opts))
			}
		})
	}
	t.Run("invalid errors", func(t *testing.T) {
		if _, err := writeModeOpts("bogus"); err == nil {
			t.Fatal("expected error for invalid write mode")
		}
	})
}

func TestKVGet_InvalidConsistencyFlag(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "kv", "get", "k", "--consistency", "bogus")
	if err == nil {
		t.Fatal("expected error for invalid --consistency value")
	}
	if !strings.Contains(err.Error(), "consistency") {
		t.Fatalf("expected consistency error, got: %v", err)
	}
}

func TestKVPut_InvalidWriteModeFlag(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "kv", "put", "k", "v", "--write-mode", "bogus")
	if err == nil {
		t.Fatal("expected error for invalid --write-mode value")
	}
}

func TestKVGet_EventualConsistency_Succeeds(t *testing.T) {
	mvccSvc := newMockMVCCService()
	mvccSvc.store["ck"] = []byte("cv")
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "kv", "get", "ck", "--consistency", "eventual")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "cv") {
		t.Fatalf("expected value cv in output, got: %q", out)
	}
}
