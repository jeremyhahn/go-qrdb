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
	infra := n.withHolderInfra(lifecycle.Infra{}, nil, []server.ServerOption{server.WithAuditLogger(trail)})
	if got, _ := infra.AuditLogger.(*audit.RingBuffer); got != trail {
		t.Fatalf("Infra.AuditLogger = %T, want the caller's trail", infra.AuditLogger)
	}
}

// Without a caller trail the infra carries none.
func TestWithHolderInfra_NoTrailCarriesNone(t *testing.T) {
	n := &Node{}
	if infra := n.withHolderInfra(lifecycle.Infra{}, nil, nil); infra.AuditLogger != nil {
		t.Fatalf("Infra.AuditLogger = %T, want nil", infra.AuditLogger)
	}
}

// The holder authority follows the node's auth.audit_failure_policy; an
// absent or unrecognised value is the secure default, fail-closed.
func TestWithHolderInfra_CarriesTheAuditPosture(t *testing.T) {
	n := &Node{}
	for value, want := range map[string]audit.FailurePolicy{
		"":            audit.FailClosed,
		"fail-open":   audit.FailOpen,
		"fail-closed": audit.FailClosed,
		"fail_open":   audit.FailClosed,
	} {
		cfg := qrdbconfig.DefaultConfig()
		cfg.Auth.AuditFailurePolicy = value
		if got := n.withHolderInfra(lifecycle.Infra{}, cfg, nil).AuditFailurePolicy; got != want {
			t.Fatalf("policy %q = %v, want %v", value, got, want)
		}
	}
}
