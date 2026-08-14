//go:build !integration

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

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// lockInfoRow
// ---------------------------------------------------------------------------

func TestLockInfoRow_Fields(t *testing.T) {
	info := &transport.LockInfo{
		Key:          "mylock",
		FencingToken: 42,
		LeaseID:      7,
		HolderID:     "node-1",
		Reentrant:    true,
		AcquireCount: 3,
		TTL:          60,
		AcquiredAt:   1000000,
	}

	row := lockInfoRow(info)

	if len(row) == 0 {
		t.Fatal("expected non-empty row")
	}
	if row[0] != "mylock" {
		t.Errorf("row[0]: expected %q, got %q", "mylock", row[0])
	}
	if row[2] != "node-1" {
		t.Errorf("row[2]: expected %q, got %q", "node-1", row[2])
	}
}

func TestLockInfoRow_EmptyInfo(t *testing.T) {
	info := &transport.LockInfo{}
	row := lockInfoRow(info)
	// Must not panic and must return a slice.
	if row == nil {
		t.Fatal("expected non-nil row for empty LockInfo")
	}
}

// ---------------------------------------------------------------------------
// printLockInfo
// ---------------------------------------------------------------------------

func TestPrintLockInfo_JSON(t *testing.T) {
	buf := &bytes.Buffer{}
	originalWriter := outputWriter
	originalFormat := outputFormat
	defer func() {
		outputWriter = originalWriter
		outputFormat = originalFormat
	}()

	outputWriter = buf
	outputFormat = "json"

	info := &transport.LockInfo{
		Key:          "lock-a",
		FencingToken: 1,
		HolderID:     "node-1",
		Reentrant:    false,
		AcquireCount: 1,
		TTL:          30,
	}

	if err := printLockInfo(info); err != nil {
		t.Fatalf("printLockInfo(json): %v", err)
	}

	var decoded map[string]interface{}
	if err := json.NewDecoder(buf).Decode(&decoded); err != nil {
		t.Fatalf("failed to decode JSON output: %v", err)
	}
	// The JSON must contain the key field.
	if decoded["key"] != "lock-a" && decoded["Key"] != "lock-a" {
		t.Errorf("expected key=lock-a in JSON output, got %v", decoded)
	}
}

func TestPrintLockInfo_Text(t *testing.T) {
	buf := &bytes.Buffer{}
	originalWriter := outputWriter
	originalFormat := outputFormat
	defer func() {
		outputWriter = originalWriter
		outputFormat = originalFormat
	}()

	outputWriter = buf
	outputFormat = "text"

	info := &transport.LockInfo{
		Key:          "lock-b",
		FencingToken: 99,
		HolderID:     "holder-x",
		TTL:          120,
	}

	if err := printLockInfo(info); err != nil {
		t.Fatalf("printLockInfo(text): %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "lock-b") {
		t.Errorf("expected %q in text output, got %q", "lock-b", out)
	}
	if !strings.Contains(out, "holder-x") {
		t.Errorf("expected %q in text output, got %q", "holder-x", out)
	}
}
