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

package server

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"

	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// stubRegistry records which resolutions were forwarded to the inner
// registry.
type stubRegistry struct {
	service.Registry
	calls []string
}

func (s *stubRegistry) KVService(tenant, db string) (service.KVServicer, error) {
	s.calls = append(s.calls, "kv")
	return nil, nil
}

// TransactionService is overridden because the gate now PASSES IT THROUGH
// (owner ruling 2026-08-23, go-qrdb#62). stubRegistry embeds a nil
// service.Registry, so a resolution the gate forwards without a method here
// panics rather than failing an assertion.
func (s *stubRegistry) TransactionService(tenant, db string) (service.TransactionServicer, error) {
	s.calls = append(s.calls, "transaction")
	return nil, nil
}

func (s *stubRegistry) IndexService(tenant, db string) (service.IndexServicer, error) {
	s.calls = append(s.calls, "index")
	return nil, nil
}

func (s *stubRegistry) TimeSeriesService(tenant, db string) (service.TimeSeriesServicer, error) {
	s.calls = append(s.calls, "timeseries")
	return nil, nil
}

func (s *stubRegistry) VectorService(tenant, db string) (service.VectorServicer, error) {
	s.calls = append(s.calls, "vector")
	return nil, nil
}

func (s *stubRegistry) BatchService(tenant, db string) (service.BatchServicer, error) {
	s.calls = append(s.calls, "batch")
	return nil, nil
}

func (s *stubRegistry) TableService(tenant, db string) (service.TableServicer, error) {
	s.calls = append(s.calls, "table")
	return nil, nil
}

func (s *stubRegistry) SequenceService(tenant, db string) (service.SequenceServicer, error) {
	s.calls = append(s.calls, "sequence")
	return nil, nil
}

func (s *stubRegistry) PrivilegeService(tenant, db string) (service.PrivilegeServicer, error) {
	s.calls = append(s.calls, "privilege")
	return nil, nil
}

func (s *stubRegistry) CollectionService(tenant, db string) (service.CollectionServicer, error) {
	s.calls = append(s.calls, "collection")
	return nil, nil
}

// TestRaftOnlyGatedRegistry_RefusesRaftOnlyFamilies pins the standalone
// API gate: the raft-only registry resolutions are refused with the typed
// hinted error and NEVER reach the inner registry.
func TestRaftOnlyGatedRegistry_RefusesRaftOnlyFamilies(t *testing.T) {
	inner := &stubRegistry{}
	gated := &raftOnlyGatedRegistry{inner: inner}

	cases := []struct {
		name string
		call func() error
	}{
		{"mvcc", func() error { _, err := gated.MVCCService("t", "db"); return err }},
		{"watch", func() error { _, err := gated.WatchService("t", "db"); return err }},
		{"lease", func() error { _, err := gated.LeaseService("t", "db"); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			// A SERVICE-layer refusal, the one every transport classifies as
			// "this node cannot do that" (go-qrdb#473) — not a REST type that
			// only REST recognised.
			var cue *service.CapabilityUnsupportedError
			if !errors.As(err, &cue) {
				t.Fatalf("got %T (%v), want *service.CapabilityUnsupportedError", err, err)
			}
			if !strings.HasPrefix(cue.Message, tc.name+": ") {
				t.Errorf("message %q must name the refused service %q", cue.Message, tc.name)
			}
			if !strings.Contains(err.Error(), StandaloneModeHint) {
				t.Errorf("error %q must carry the hint %q", err.Error(), StandaloneModeHint)
			}
		})
	}
	if len(inner.calls) != 0 {
		t.Fatalf("raft-only resolutions leaked to the inner registry: %v", inner.calls)
	}
}

// TestRaftOnlyGatedRegistry_PassesThroughTransactions covers the owner ruling
// of 2026-08-23 on go-qrdb#62, and replaces this family's row in the refusal
// table above.
//
// The transaction/CAS family was refused here with "requires mode: raft". It
// is a standard database operation that a single-process engine-direct node
// serves correctly -- consensus is what makes a compare-and-swap linearizable
// across REPLICAS, and a standalone node has none. Withholding it left a
// standalone deployment with no compare-and-swap on any surface, which is why
// go-xkms could not boot.
//
// Asserted separately from the pass-through table below because this one is a
// deliberate reversal: a future change that re-gated it would otherwise only
// break an entry in a list of eight.
func TestRaftOnlyGatedRegistry_PassesThroughTransactions(t *testing.T) {
	inner := &stubRegistry{}
	gated := &raftOnlyGatedRegistry{inner: inner}

	if _, err := gated.TransactionService("t", "db"); err != nil {
		t.Fatalf("TransactionService = %v; want nil -- standalone serves CAS", err)
	}
	if len(inner.calls) != 1 || inner.calls[0] != "transaction" {
		t.Fatalf("inner calls = %v; want [transaction] -- the resolution must reach "+
			"the real registry, not be swallowed", inner.calls)
	}
}

// TestRaftOnlyGatedRegistry_PassesThroughStandardAndSQL pins the flip
// side: standard operations AND the SQL catalog resolutions (which
// consume MVCC internally) flow through untouched.
func TestRaftOnlyGatedRegistry_PassesThroughStandardAndSQL(t *testing.T) {
	inner := &stubRegistry{}
	gated := &raftOnlyGatedRegistry{inner: inner}

	if _, err := gated.KVService("t", "db"); err != nil {
		t.Fatal(err)
	}
	if _, err := gated.BatchService("t", "db"); err != nil {
		t.Fatal(err)
	}
	if _, err := gated.IndexService("t", "db"); err != nil {
		t.Fatal(err)
	}
	if _, err := gated.TimeSeriesService("t", "db"); err != nil {
		t.Fatal(err)
	}
	if _, err := gated.VectorService("t", "db"); err != nil {
		t.Fatal(err)
	}
	if _, err := gated.TableService("t", "db"); err != nil {
		t.Fatal(err)
	}
	if _, err := gated.SequenceService("t", "db"); err != nil {
		t.Fatal(err)
	}
	if _, err := gated.PrivilegeService("t", "db"); err != nil {
		t.Fatal(err)
	}
	if _, err := gated.CollectionService("t", "db"); err != nil {
		t.Fatal(err)
	}

	want := []string{"kv", "batch", "index", "timeseries", "vector", "table", "sequence", "privilege", "collection"}
	if len(inner.calls) != len(want) {
		t.Fatalf("inner calls = %v, want %v", inner.calls, want)
	}
	for i, w := range want {
		if inner.calls[i] != w {
			t.Fatalf("inner calls = %v, want %v", inner.calls, want)
		}
	}
}

// TestTransportServiceRegistry_ModeSelection verifies the server hands the
// gated registry to transports only under WithStandaloneMode.
func TestTransportServiceRegistry_ModeSelection(t *testing.T) {
	dbRegistry := NewDatabaseServiceRegistry("system")
	dbRegistry.Register("system", &DatabaseServices{Name: "system"})

	base := []ServerOption{WithDatabaseServiceRegistry(dbRegistry)}

	raftSrv := New(testConfig(t, false, false), nil, nil, base...)
	if _, gated := raftSrv.transportServiceRegistry().(*raftOnlyGatedRegistry); gated {
		t.Fatal("raft mode must hand transports the ungated registry")
	}

	saSrv := New(testConfig(t, false, false), nil, nil, append(base, WithStandaloneMode())...)
	if _, gated := saSrv.transportServiceRegistry().(*raftOnlyGatedRegistry); !gated {
		t.Fatal("standalone mode must hand transports the gated registry")
	}
	// SQL keeps the ungated registry in both modes.
	if _, gated := saSrv.buildServiceRegistry().(*raftOnlyGatedRegistry); gated {
		t.Fatal("the SQL-facing registry must stay ungated in standalone mode")
	}
}

// TestStandaloneGate_RefusalIsUnimplementedOnEveryClassifier is the assertion
// go-qrdb#473 turns on. The gate's refusal used to be *rest.ServiceNotImplementedError,
// which only REST classified: REST answered 501 while gRPC, unix, QUIC and MCP
// fell through to their unclassified default and told a caller its server had
// broken for a condition that is a deployment mode. The refusal is now the
// service-layer error every transport maps to "this node cannot do that". This
// runs the gate's ACTUAL error — not a hand-built one — through the shared
// classifier the gRPC and MCP transports answer from, and asserts the hint
// text survives; the per-transport tables in pkg/api pin the QUIC status and
// the REST code for the same condition.
func TestStandaloneGate_RefusalIsUnimplementedOnEveryClassifier(t *testing.T) {
	gated := &raftOnlyGatedRegistry{inner: &stubRegistry{}}
	_, err := gated.MVCCService("t", "db")
	if err == nil {
		t.Fatal("standalone gate did not refuse mvcc")
	}
	if got := grpcapi.ServiceErrorCode(err); got != codes.Unimplemented {
		t.Fatalf("shared classifier answers %s for the standalone gate's refusal, want Unimplemented", got)
	}
	if !strings.Contains(err.Error(), StandaloneModeHint) {
		t.Fatalf("the hint %q did not survive: %q", StandaloneModeHint, err.Error())
	}
	// The premise #473 closed from the service side is now closed from the
	// transport side too: since go-qrdb#557 every transport classifies through
	// pkg/api/errclass, and the REST type declares its own class there, so even
	// a reintroduced REST-typed refusal would reach a gRPC caller as
	// Unimplemented rather than as a server fault.
	if got := grpcapi.ServiceErrorCode(&rest.ServiceNotImplementedError{Service: "mvcc", Hint: StandaloneModeHint}); got != codes.Unimplemented {
		t.Fatalf("the shared classifier answers %s for the REST refusal type, want Unimplemented", got)
	}
}
