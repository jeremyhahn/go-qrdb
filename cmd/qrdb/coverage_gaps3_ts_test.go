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

//go:build qrdb_ts && !integration

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// newTSWriteBatchCmd — stdin read and invalid JSON branches (76.2%)
// ===========================================================================

// TestTSWriteBatchCmd_StdinSuccess exercises the stdin read path ("-").
func TestTSWriteBatchCmd_StdinSuccess(t *testing.T) {
	mvccSvc := newMockMVCCService()
	tsSvc := &mockTimeSeriesService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, tsSvc)
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	points := []transport.TSDataPoint{
		{Metric: "cpu", Value: 1.5, TimestampNs: 1000},
	}
	data, _ := json.Marshal(points)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
	})

	if _, writeErr := w.Write(data); writeErr != nil {
		t.Fatalf("write to pipe: %v", writeErr)
	}
	_ = w.Close()

	out, cmdErr := executeCommand(root, "ts", "write-batch", "--file", "-")
	if cmdErr != nil {
		t.Fatalf("expected no error from stdin ts write-batch, got: %v\noutput: %s", cmdErr, out)
	}
}

// TestTSWriteBatchCmd_InvalidJSON verifies that malformed JSON in a file
// returns an error.
func TestTSWriteBatchCmd_InvalidJSON(t *testing.T) {
	mvccSvc := newMockMVCCService()
	tsSvc := &mockTimeSeriesService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, tsSvc)
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	tmpFile := filepath.Join(t.TempDir(), "ts-batch.json")
	if err := os.WriteFile(tmpFile, []byte("{bad json!!"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, cmdErr := executeCommand(root, "ts", "write-batch", "--file", tmpFile)
	if cmdErr == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
