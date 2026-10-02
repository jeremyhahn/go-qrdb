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
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// nodeCompactionAdminService holds the node-keyspace policy the way the
// admin service does: validated on set, off stored as none.
type nodeCompactionAdminService struct {
	topoAdminService
	policy *compaction.Policy
	err    error
}

func (m *nodeCompactionAdminService) NodeCompaction(context.Context) (*compaction.Policy, error) {
	return m.policy, m.err
}

func (m *nodeCompactionAdminService) SetNodeCompaction(_ context.Context, p *compaction.Policy) error {
	if m.err != nil {
		return m.err
	}
	if p != nil {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	m.policy = p.Normalized()
	return nil
}

var _ service.AdminServicer = (*nodeCompactionAdminService)(nil)

func TestAdminNodeCompaction_SetShowAndTurnOff(t *testing.T) {
	svc := &nodeCompactionAdminService{}

	out, err := executeAdminCmd(t, svc, "node-compaction")
	if err != nil || !strings.Contains(out, "Compaction: off") {
		t.Fatalf("node-compaction before any policy = %q, %v; want off", out, err)
	}

	out, err = executeAdminCmd(t, svc, "update-node-compaction", "--mode", "revision", "--retain-revisions", "500")
	if err != nil {
		t.Fatalf("update-node-compaction: %v", err)
	}
	if !strings.Contains(out, "Mode:     revision") || !strings.Contains(out, "500 revisions") {
		t.Fatalf("update-node-compaction output = %q; want the revision policy", out)
	}
	if svc.policy == nil || svc.policy.RetainRevisions != 500 {
		t.Fatalf("policy held = %+v; want 500 revisions", svc.policy)
	}

	oldFormat := outputFormat
	outputFormat = "json"
	out, err = executeAdminCmd(t, svc, "node-compaction")
	outputFormat = oldFormat
	if err != nil {
		t.Fatalf("node-compaction (json): %v", err)
	}
	var shown struct {
		Compaction *struct {
			Mode            string `json:"mode"`
			RetainRevisions int64  `json:"retainRevisions"`
		} `json:"compaction"`
	}
	if err := json.Unmarshal([]byte(out), &shown); err != nil || shown.Compaction == nil || shown.Compaction.RetainRevisions != 500 {
		t.Fatalf("node-compaction json = %q (%v); want the policy", out, err)
	}

	out, err = executeAdminCmd(t, svc, "update-node-compaction", "--mode", "period", "--retain-period", "720h")
	if err != nil || !strings.Contains(out, "720h0m0s") {
		t.Fatalf("update-node-compaction (period) = %q, %v", out, err)
	}

	out, err = executeAdminCmd(t, svc, "update-node-compaction", "--mode", "off")
	if err != nil || !strings.Contains(out, "Compaction: off") || svc.policy != nil {
		t.Fatalf("turning it off = %q, %v (held %+v)", out, err, svc.policy)
	}
}

func TestAdminNodeCompaction_Refusals(t *testing.T) {
	svc := &nodeCompactionAdminService{}
	if _, err := executeAdminCmd(t, svc, "update-node-compaction"); err == nil {
		t.Fatal("update-node-compaction without --mode must fail")
	}
	_, err := executeAdminCmd(t, svc, "update-node-compaction", "--mode", "revision")
	var invalid *compaction.InvalidPolicyError
	if !errors.As(err, &invalid) {
		t.Fatalf("revision without a count = %v; want *compaction.InvalidPolicyError", err)
	}

	down := &nodeCompactionAdminService{err: &service.NodeCompactionUnavailableError{}}
	if _, err := executeAdminCmd(t, down, "node-compaction"); err == nil {
		t.Fatal("node-compaction on a node without the table must fail")
	}
	if _, err := executeAdminCmd(t, down, "update-node-compaction", "--mode", "off"); err == nil {
		t.Fatal("update-node-compaction on a node without the table must fail")
	}
}
