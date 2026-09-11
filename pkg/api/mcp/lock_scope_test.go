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

package mcp

import (
	"context"
	"testing"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// scopedLockStub is the narrowest LockServicer that can answer "which servicer
// did the tool reach, and with what?".
type scopedLockStub struct {
	service.LockServicer
	calls []string
}

func (s *scopedLockStub) Lock(_ context.Context, key, holderID string, _ time.Duration, _ bool) (*lock.Lock, error) {
	s.calls = append(s.calls, "lock:"+key)
	return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 1}, nil
}

func (s *scopedLockStub) TryLock(_ context.Context, key, holderID string, _ time.Duration, _ bool) (*lock.Lock, error) {
	s.calls = append(s.calls, "trylock:"+key)
	return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 1}, nil
}

func (s *scopedLockStub) Unlock(_ context.Context, key string, _ int64) error {
	s.calls = append(s.calls, "unlock:"+key)
	return nil
}

func (s *scopedLockStub) Extend(_ context.Context, key string, _ int64) (*lock.Lock, error) {
	s.calls = append(s.calls, "extend:"+key)
	return &lock.Lock{Key: key, FencingToken: 2}, nil
}

func (s *scopedLockStub) Status(_ context.Context, key string) (*lock.Lock, error) {
	s.calls = append(s.calls, "status:"+key)
	return &lock.Lock{Key: key, HolderID: "w1", FencingToken: 1}, nil
}

func (s *scopedLockStub) List(_ context.Context, prefix string) ([]*lock.Lock, error) {
	s.calls = append(s.calls, "list:"+prefix)
	return nil, nil
}

// lockToolCases is every lock tool, so the assertions cover the whole surface
// rather than one verb -- an unresolved verb is an unconfined verb. The
// handler field's type is the compile-time check that every lock tool still
// takes the singleton as a FALLBACK rather than as its only servicer.
var lockToolCases = []struct {
	name    string
	tool    string
	args    map[string]any
	handler func(service.LockServicer) mcpserver.ToolHandlerFunc
	want    string
}{
	{"acquire", "qrdb_lock_acquire",
		map[string]any{"key": "deploy", "holder_id": "w1", "ttl": 60},
		handleLockAcquire, "lock:deploy"},
	{"trylock", "qrdb_lock_trylock",
		map[string]any{"key": "deploy", "holder_id": "w1", "ttl": 60},
		handleLockTryLock, "trylock:deploy"},
	{"release", "qrdb_lock_release",
		map[string]any{"key": "deploy", "fencing_token": 1},
		handleLockRelease, "unlock:deploy"},
	{"extend", "qrdb_lock_extend",
		map[string]any{"key": "deploy", "fencing_token": 1},
		handleLockExtend, "extend:deploy"},
	{"status", "qrdb_lock_status",
		map[string]any{"key": "deploy"},
		handleLockStatus, "status:deploy"},
	{"list", "qrdb_lock_list",
		map[string]any{"prefix": "jobs/"},
		handleLockList, "list:jobs/"},
}

// TestMCPLockResolvesThroughTheRegistry holds the transport half of
// go-qrdb#455 on MCP: with a registry bound to the call context, every lock
// tool must resolve the servicer for the caller's (tenant, database) scope and
// must not reach the captured singleton, which is built over the raw,
// un-namespaced engines.
func TestMCPLockResolvesThroughTheRegistry(t *testing.T) {
	for _, c := range lockToolCases {
		t.Run(c.name, func(t *testing.T) {
			singleton := &scopedLockStub{}
			scoped := &scopedLockStub{}
			reg := &recordingRegistry{lock: scoped}

			ctx := withRegistry(tenant.WithTenantID(context.Background(), "acme"), reg)
			ctx = tenant.WithDatabase(ctx, "db2")
			req := buildCallToolRequest(t, c.tool, c.args)

			if _, err := c.handler(singleton)(ctx, req); err != nil {
				t.Fatalf("%s: %v", c.tool, err)
			}

			if len(singleton.calls) != 0 {
				t.Fatalf("the captured singleton was used: %v", singleton.calls)
			}
			if len(scoped.calls) != 1 || scoped.calls[0] != c.want {
				t.Fatalf("scoped servicer saw %v, want [%s]", scoped.calls, c.want)
			}
			if len(reg.calls) != 1 {
				t.Fatalf("registry calls = %v, want exactly one", reg.calls)
			}
			got := reg.calls[0]
			if got.service != "lock" || got.tenant != "acme" || got.db != "db2" {
				t.Fatalf("registry resolved %+v, want {lock acme db2}", got)
			}
		})
	}
}

// TestMCPLockFallsBackToTheSingleton covers the registry-less path, which is
// the single-database, embedded and unit-test composition.
func TestMCPLockFallsBackToTheSingleton(t *testing.T) {
	for _, c := range lockToolCases {
		t.Run(c.name, func(t *testing.T) {
			singleton := &scopedLockStub{}
			req := buildCallToolRequest(t, c.tool, c.args)
			if _, err := c.handler(singleton)(context.Background(), req); err != nil {
				t.Fatalf("%s: %v", c.tool, err)
			}
			if len(singleton.calls) != 1 || singleton.calls[0] != c.want {
				t.Fatalf("singleton saw %v, want [%s]", singleton.calls, c.want)
			}
		})
	}
}

// TestMCPLockWithNeitherIsRefused pins the fail-closed case: no registry bound
// to ctx and no captured singleton reports not-configured rather than
// dereferencing nil.
func TestMCPLockWithNeitherIsRefused(t *testing.T) {
	if _, err := resolveLock(context.Background(), nil); err == nil {
		t.Fatal("expected an error when neither a registry nor a singleton is configured")
	} else if _, ok := err.(*ServiceNotConfiguredError); !ok {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}

	for _, c := range lockToolCases {
		t.Run(c.name, func(t *testing.T) {
			req := buildCallToolRequest(t, c.tool, c.args)
			res, err := c.handler(nil)(context.Background(), req)
			if err != nil {
				t.Fatalf("%s returned a transport error rather than a tool error: %v", c.tool, err)
			}
			if res == nil || !res.IsError {
				t.Fatalf("%s answered without a lock servicer: %+v", c.tool, res)
			}
		})
	}
}
