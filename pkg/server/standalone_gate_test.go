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
	"strings"
	"testing"

	"google.golang.org/grpc/codes"

	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// coordinationServices is a database bundle carrying every engine the
// coordination families resolve through. The servicers hold no host: the
// test asserts what the registry RESOLVES, never calls through them.
func coordinationServices(t *testing.T) *DatabaseServices {
	t.Helper()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	return &DatabaseServices{
		Name:        "system",
		KV:          service.NewKVService(nil, 1),
		MVCC:        service.NewMVCCKVService(nil, 1),
		Lease:       service.NewLeaseService(nil, 1),
		Watch:       service.NewWatchService(hub),
		Transaction: service.NewTransactionService(nil, 1),
	}
}

// TestStandaloneMode_ServesTheCoordinationFamilies pins go-qrdb#582's
// reversal: a standalone server's transports resolve MVCC, watch, lease and
// lock through the same registry raft mode uses. Under the old gate every one
// of these four answered "requires mode: raft" on every transport.
func TestStandaloneMode_ServesTheCoordinationFamilies(t *testing.T) {
	for _, mode := range []struct {
		name string
		opts []ServerOption
	}{
		{"raft", nil},
		{"standalone", []ServerOption{WithStandaloneMode()}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			dbRegistry := NewDatabaseServiceRegistry("system")
			dbRegistry.Register("system", coordinationServices(t))
			srv := New(testConfig(t, false, false), nil, nil, append([]ServerOption{WithDatabaseServiceRegistry(dbRegistry)}, mode.opts...)...)
			reg := srv.ServiceRegistry()

			calls := map[string]func() error{
				"mvcc":  func() error { _, err := reg.MVCCService("system", "system"); return err },
				"watch": func() error { _, err := reg.WatchService("system", "system"); return err },
				"lease": func() error { _, err := reg.LeaseService("system", "system"); return err },
				"lock":  func() error { _, err := reg.LockService("system", "system"); return err },
			}
			for family, call := range calls {
				if err := call(); err != nil {
					t.Errorf("%s: %s resolution refused: %v", mode.name, family, err)
				}
			}
		})
	}
}

// TestStandaloneMode_EtcdRefusalIsUnimplementedOnEveryClassifier: the etcd
// compatibility listener stays raft-only, and its refusal is the
// service-layer error every transport maps to "this node cannot do that"
// (go-qrdb#473), with the remedy text intact.
func TestStandaloneMode_EtcdRefusalIsUnimplementedOnEveryClassifier(t *testing.T) {
	err := notImplemented("etcd", StandaloneModeHint)
	if got := grpcapi.ServiceErrorCode(err); got != codes.Unimplemented {
		t.Fatalf("shared classifier answers %s for the standalone refusal, want Unimplemented", got)
	}
	if !strings.Contains(err.Error(), StandaloneModeHint) {
		t.Fatalf("the hint %q did not survive: %q", StandaloneModeHint, err.Error())
	}
	// Since go-qrdb#557 every transport classifies through pkg/api/errclass,
	// and the REST type declares its own class there, so even a reintroduced
	// REST-typed refusal reaches a gRPC caller as Unimplemented.
	if got := grpcapi.ServiceErrorCode(&rest.ServiceNotImplementedError{Service: "etcd", Hint: StandaloneModeHint}); got != codes.Unimplemented {
		t.Fatalf("the shared classifier answers %s for the REST refusal type, want Unimplemented", got)
	}
}

// TestNotImplemented_MessageNamesServiceAndHint pins the refusal text.
func TestNotImplemented_MessageNamesServiceAndHint(t *testing.T) {
	if got := notImplemented("lock", "").Error(); !strings.Contains(got, "lock: not implemented on this server") || strings.Contains(got, StandaloneModeHint) {
		t.Fatalf("unhinted refusal = %q", got)
	}
	if got := notImplemented("etcd", StandaloneModeHint).Error(); !strings.HasSuffix(got, ": "+StandaloneModeHint) {
		t.Fatalf("hinted refusal = %q", got)
	}
}
