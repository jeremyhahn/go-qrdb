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
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
)

func TestDatabaseUpdateCompaction_Command(t *testing.T) {
	svc := newRealCLIDatabaseService(t)
	if _, err := executeDatabaseCmd(t, svc, "create", "events", "--backend", "memory"); err != nil {
		t.Fatalf("create error: %v", err)
	}

	output, err := executeDatabaseCmd(t, svc, "update-compaction", "events", "--mode", "revision", "--retain-revisions", "100")
	if err != nil {
		t.Fatalf("update-compaction error: %v", err)
	}
	if !strings.Contains(output, "Mode:     revision") || !strings.Contains(output, "100 revisions") {
		t.Fatalf("output missing the policy: %q", output)
	}

	output, err = executeDatabaseCmd(t, svc, "update-compaction", "events", "--mode", "period", "--retain-period", "24h")
	if err != nil {
		t.Fatalf("update-compaction(period) error: %v", err)
	}
	if !strings.Contains(output, "24h0m0s") {
		t.Fatalf("output missing the period: %q", output)
	}
	info, err := svc.Get("events")
	if err != nil || info.Compaction == nil || info.Compaction.RetainDuration().Hours() != 24 {
		t.Fatalf("stored policy = %+v, %v", info.Compaction, err)
	}

	_, err = executeDatabaseCmd(t, svc, "update-compaction", "events", "--mode", "revision")
	var invalid *compaction.InvalidPolicyError
	if !errors.As(err, &invalid) {
		t.Fatalf("revision without a count = %v, want *InvalidCompactionPolicyError", err)
	}

	if _, err := executeDatabaseCmd(t, svc, "update-compaction", "events"); err == nil {
		t.Fatal("update-compaction without --mode must fail")
	}

	output, err = executeDatabaseCmd(t, svc, "update-compaction", "events", "--mode", "off")
	if err != nil || strings.Contains(output, "Compaction:") {
		t.Fatalf("turning it off = %q, %v", output, err)
	}
}
