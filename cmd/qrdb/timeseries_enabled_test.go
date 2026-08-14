//go:build qrdb_ts

// Copyright 2026 Jeremy Hahn
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

package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// TimeSeries command tests
// ---------------------------------------------------------------------------

func TestTSWrite(t *testing.T) {
	mvccSvc := newMockMVCCService()
	tsSvc := &mockTimeSeriesService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, tsSvc)
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "ts", "write", "--metric", "cpu.usage", "--value", "75.5")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK output, got: %q", out)
	}
}

func TestTSWriteWithTags(t *testing.T) {
	mvccSvc := newMockMVCCService()
	tsSvc := &mockTimeSeriesService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, tsSvc)
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "ts", "write", "--metric", "cpu.usage", "--value", "75.5", "--tag", "host=server1", "--tag", "dc=us-east")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK output, got: %q", out)
	}
}

func TestTSWriteMissingMetric(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "ts", "write", "--value", "75.5")
	if err == nil {
		t.Fatal("expected error for missing --metric flag")
	}
}

func TestTSWriteMissingValue(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "ts", "write", "--metric", "cpu.usage")
	if err == nil {
		t.Fatal("expected error for missing --value flag")
	}
}

func TestTSQuery(t *testing.T) {
	mvccSvc := newMockMVCCService()
	tsSvc := &mockTimeSeriesService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, tsSvc)
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "ts", "query", "--metric", "cpu.usage", "--start", "1000000000", "--end", "3000000000")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "TIMESTAMP_NS") {
		t.Fatalf("expected TIMESTAMP_NS header, got: %q", out)
	}
	if !strings.Contains(out, "cpu.usage") {
		t.Fatalf("expected cpu.usage in output, got: %q", out)
	}
}

func TestTSQueryJSON(t *testing.T) {
	mvccSvc := newMockMVCCService()
	tsSvc := &mockTimeSeriesService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, tsSvc)
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "--output", "json", "ts", "query", "--metric", "cpu.usage", "--start", "1000000000", "--end", "3000000000")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"points"`) {
		t.Fatalf("expected JSON points field, got: %q", out)
	}
}

func TestTSQueryMissingMetric(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "ts", "query", "--start", "1000000000", "--end", "3000000000")
	if err == nil {
		t.Fatal("expected error for missing --metric flag")
	}
}

func TestTSAggregate(t *testing.T) {
	mvccSvc := newMockMVCCService()
	tsSvc := &mockTimeSeriesService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, tsSvc)
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "ts", "aggregate", "--metric", "cpu.usage", "--start", "1000000000", "--end", "3000000000", "--function", "avg")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "TIMESTAMP_NS") {
		t.Fatalf("expected TIMESTAMP_NS header, got: %q", out)
	}
	if !strings.Contains(out, "COUNT") {
		t.Fatalf("expected COUNT header, got: %q", out)
	}
}

func TestTSAggregateJSON(t *testing.T) {
	mvccSvc := newMockMVCCService()
	tsSvc := &mockTimeSeriesService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, tsSvc)
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "--output", "json", "ts", "aggregate", "--metric", "cpu.usage", "--start", "1000000000", "--end", "3000000000", "--function", "sum")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, `"results"`) {
		t.Fatalf("expected JSON results field, got: %q", out)
	}
}

func TestTSAggregateInvalidFunction(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "ts", "aggregate", "--metric", "cpu.usage", "--start", "1000000000", "--end", "3000000000", "--function", "bogus")
	if err == nil {
		t.Fatal("expected error for invalid aggregation function")
	}
	var iae *InvalidAggFuncError
	if !errors.As(err, &iae) {
		t.Fatalf("expected InvalidAggFuncError, got: %T: %v", err, err)
	}
}

func TestTSAggregateMissingFunction(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "ts", "aggregate", "--metric", "cpu.usage", "--start", "1000000000", "--end", "3000000000")
	if err == nil {
		t.Fatal("expected error for missing --function flag")
	}
}

// ---------------------------------------------------------------------------
// Aggregation function map tests (table-driven)
// ---------------------------------------------------------------------------

func TestTSAggregateAllFunctions(t *testing.T) {
	functions := []struct {
		name string
		want transport.AggFunc
	}{
		{"avg", transport.AggAvg},
		{"sum", transport.AggSum},
		{"min", transport.AggMin},
		{"max", transport.AggMax},
		{"count", transport.AggCount},
		{"percentile", transport.AggPercentile},
	}

	for _, tt := range functions {
		t.Run(tt.name, func(t *testing.T) {
			mvccSvc := newMockMVCCService()
			tsSvc := &mockTimeSeriesService{}
			opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, tsSvc)
			root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

			_, err := executeCommand(root, "ts", "aggregate", "--metric", "m", "--start", "0", "--end", "1", "--function", tt.name)
			if err != nil {
				t.Fatalf("expected no error for function %q, got: %v", tt.name, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseTags tests (table-driven)
// ---------------------------------------------------------------------------

func TestParseTags(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  int
	}{
		{"nil input", nil, 0},
		{"empty input", []string{}, 0},
		{"single valid tag", []string{"host=server1"}, 1},
		{"multiple valid tags", []string{"host=server1", "dc=us-east"}, 2},
		{"malformed tag without equals", []string{"noequalssign"}, 0},
		{"mixed valid and malformed", []string{"host=server1", "noeq", "dc=west"}, 2},
		{"value with equals", []string{"url=http://example.com?k=v"}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTags(tt.input)
			if len(got) != tt.want {
				t.Errorf("parseTags(%v) returned %d tags, want %d", tt.input, len(got), tt.want)
			}
		})
	}
}

func TestParseTagsKeyValueCorrectness(t *testing.T) {
	tags := parseTags([]string{"host=server1", "env=prod"})
	if len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(tags))
	}

	found := make(map[string]string)
	for _, tag := range tags {
		found[tag.Key] = tag.Value
	}

	if found["host"] != "server1" {
		t.Errorf("expected host=server1, got host=%s", found["host"])
	}
	if found["env"] != "prod" {
		t.Errorf("expected env=prod, got env=%s", found["env"])
	}
}

func TestParseTagsValueContainingEquals(t *testing.T) {
	tags := parseTags([]string{"filter=a=b=c"})
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tags))
	}
	if tags[0].Key != "filter" {
		t.Errorf("expected key 'filter', got %q", tags[0].Key)
	}
	if tags[0].Value != "a=b=c" {
		t.Errorf("expected value 'a=b=c', got %q", tags[0].Value)
	}
}

// ---------------------------------------------------------------------------
// aggFuncMap lookup tests (table-driven)
// ---------------------------------------------------------------------------

func TestAggFuncMap(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantOK  bool
		wantVal transport.AggFunc
	}{
		{"avg", "avg", true, transport.AggAvg},
		{"sum", "sum", true, transport.AggSum},
		{"min", "min", true, transport.AggMin},
		{"max", "max", true, transport.AggMax},
		{"count", "count", true, transport.AggCount},
		{"percentile", "percentile", true, transport.AggPercentile},
		{"unknown", "median", false, 0},
		{"empty", "", false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := aggFuncMap[tt.input]
			if ok != tt.wantOK {
				t.Errorf("aggFuncMap[%q] ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if ok && got != tt.wantVal {
				t.Errorf("aggFuncMap[%q] = %v, want %v", tt.input, got, tt.wantVal)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TimeSeries YAML output tests
// ---------------------------------------------------------------------------

func TestTSQueryYAML(t *testing.T) {
	mvccSvc := newMockMVCCService()
	tsSvc := &mockTimeSeriesService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, tsSvc)
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "--output", "yaml", "ts", "query", "--metric", "cpu.usage", "--start", "1000000000", "--end", "3000000000")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "points:") {
		t.Fatalf("expected YAML points field, got: %q", out)
	}
}

func TestTSAggregateYAML(t *testing.T) {
	mvccSvc := newMockMVCCService()
	tsSvc := &mockTimeSeriesService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, tsSvc)
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "--output", "yaml", "ts", "aggregate", "--metric", "cpu.usage", "--start", "1000000000", "--end", "3000000000", "--function", "avg")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "results:") {
		t.Fatalf("expected YAML results field, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// TSWriteBatch tests
// ---------------------------------------------------------------------------

func TestTSWriteBatchFromFile(t *testing.T) {
	points := []map[string]any{
		{"metric": "cpu.usage", "value": 75.5, "timestamp_ns": 1000000000},
		{"metric": "cpu.usage", "value": 82.3, "timestamp_ns": 2000000000},
	}
	data, err := json.Marshal(points)
	if err != nil {
		t.Fatalf("failed to marshal points: %v", err)
	}

	tmpFile, err := os.CreateTemp("", "ts-batch-*.json")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	if _, err := tmpFile.Write(data); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	_ = tmpFile.Close()

	mvccSvc := newMockMVCCService()
	tsSvc := &mockTimeSeriesService{}
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, tsSvc)
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	out, err := executeCommand(root, "ts", "write-batch", "--file", tmpFile.Name())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected OK output, got: %q", out)
	}
}

func TestTSWriteBatchMissingFile(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "ts", "write-batch")
	if err == nil {
		t.Fatal("expected error for missing --file flag")
	}
}

func TestTSWriteBatchFileNotFound(t *testing.T) {
	mvccSvc := newMockMVCCService()
	opts := newAllServiceOpts(mvccSvc, &mockTransactionService{}, &mockBatchService{}, newMockWatchService(), &mockLeaseService{}, &mockTimeSeriesService{})
	root := newTestRootCmdWithOpts(mvccSvc, newMockAdminService(), opts...)

	_, err := executeCommand(root, "ts", "write-batch", "--file", "/nonexistent/ts-batch.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	var fre *FileReadError
	if !errors.As(err, &fre) {
		t.Fatalf("expected FileReadError, got: %T: %v", err, err)
	}
}
