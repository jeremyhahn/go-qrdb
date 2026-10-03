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

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// The caller's compliance trail (server.WithAuditLogger) is the holder
// authority's: every graph built with the node's holder infra -- the sealed
// window's included -- records its holder events on it.
func TestWithHolderInfra_CarriesTheCallersTrail(t *testing.T) {
	trail := audit.NewRingBuffer(8)
	n := &Node{}
	infra, err := n.withHolderInfra(lifecycle.Infra{}, nil, []server.ServerOption{server.WithAuditLogger(trail)})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := infra.AuditLogger.(*audit.RingBuffer); got != trail {
		t.Fatalf("Infra.AuditLogger = %T, want the caller's trail", infra.AuditLogger)
	}
}

// Without a caller trail the infra carries none.
func TestWithHolderInfra_NoTrailCarriesNone(t *testing.T) {
	n := &Node{}
	if infra, err := n.withHolderInfra(lifecycle.Infra{}, nil, nil); err != nil || infra.AuditLogger != nil {
		t.Fatalf("Infra.AuditLogger = %T (%v), want nil", infra.AuditLogger, err)
	}
}

// The holder authority follows the node's auth.audit_failure_policy; an
// absent value is the secure default, fail-closed.
func TestWithHolderInfra_CarriesTheAuditPosture(t *testing.T) {
	n := &Node{}
	for value, want := range map[string]audit.FailurePolicy{
		"":            audit.FailClosed,
		"fail-open":   audit.FailOpen,
		"fail-closed": audit.FailClosed,
	} {
		cfg := qrdbconfig.DefaultConfig()
		cfg.Auth.AuditFailurePolicy = value
		infra, err := n.withHolderInfra(lifecycle.Infra{}, cfg, nil)
		if err != nil {
			t.Fatalf("policy %q: %v", value, err)
		}
		if infra.AuditFailurePolicy != want {
			t.Fatalf("policy %q = %v, want %v", value, infra.AuditFailurePolicy, want)
		}
	}
}

// An unrecognised auth.audit_failure_policy refuses the graph, as it refuses
// qrdbd's start, rather than resolving to either posture: the sealed window's
// holder authority is built before the authorization middleware parses it.
func TestWithHolderInfra_RefusesAnUnrecognisedPosture(t *testing.T) {
	n := &Node{}
	cfg := qrdbconfig.DefaultConfig()
	cfg.Auth.AuditFailurePolicy = "fail_open"
	_, err := n.withHolderInfra(lifecycle.Infra{}, cfg, nil)
	var invalid *audit.InvalidFailurePolicyError
	if !errors.As(err, &invalid) || invalid.Value != "fail_open" {
		t.Fatalf("policy %q = %v, want InvalidFailurePolicyError", cfg.Auth.AuditFailurePolicy, err)
	}
	if _, err := n.buildSealServices(context.Background(), lifecycle.Infra{}, cfg, nil); !errors.As(err, &invalid) {
		t.Fatalf("the sealed-window graph under policy %q = %v, want InvalidFailurePolicyError", cfg.Auth.AuditFailurePolicy, err)
	}
	if _, err := n.buildServices(context.Background(), lifecycle.Infra{}, cfg, nil); !errors.As(err, &invalid) {
		t.Fatalf("the full graph under policy %q = %v, want InvalidFailurePolicyError", cfg.Auth.AuditFailurePolicy, err)
	}
}
