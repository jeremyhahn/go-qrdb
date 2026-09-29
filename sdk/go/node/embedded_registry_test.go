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

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// Before the server exists (or while it is recycled) every resolution is the
// retryable recycle refusal, never a fallback to the node's default services.
func TestNodeServiceRegistry_NoServer(t *testing.T) {
	r := &nodeServiceRegistry{node: &Node{}}
	resolvers := map[string]func() error{
		"KV":          func() error { _, err := r.KVService("t", "d"); return err },
		"MVCC":        func() error { _, err := r.MVCCService("t", "d"); return err },
		"Watch":       func() error { _, err := r.WatchService("t", "d"); return err },
		"Lease":       func() error { _, err := r.LeaseService("t", "d"); return err },
		"Index":       func() error { _, err := r.IndexService("t", "d"); return err },
		"TimeSeries":  func() error { _, err := r.TimeSeriesService("t", "d"); return err },
		"Vector":      func() error { _, err := r.VectorService("t", "d"); return err },
		"Transaction": func() error { _, err := r.TransactionService("t", "d"); return err },
		"Batch":       func() error { _, err := r.BatchService("t", "d"); return err },
		"Table":       func() error { _, err := r.TableService("t", "d"); return err },
		"Sequence":    func() error { _, err := r.SequenceService("t", "d"); return err },
		"Privilege":   func() error { _, err := r.PrivilegeService("t", "d"); return err },
		"Collection":  func() error { _, err := r.CollectionService("t", "d"); return err },
		"Lock":        func() error { _, err := r.LockService("t", "d"); return err },
	}
	for name, resolve := range resolvers {
		var recycling *service.DataPlaneRecyclingError
		if err := resolve(); !errors.As(err, &recycling) {
			t.Errorf("%s with no server = %v, want *service.DataPlaneRecyclingError", name, err)
		}
	}
}

// With a server, resolution is the server's own registry, built once per
// server instance and rebuilt when the server is replaced.
func TestNodeServiceRegistry_ResolvesThroughServer(t *testing.T) {
	n := &Node{}
	r := &nodeServiceRegistry{node: n}

	// A server with no database registry has nothing to scope through.
	n.srv.Store(server.New(&config.Config{}, nil, nil))
	var unsupported *service.CapabilityUnsupportedError
	if _, err := r.KVService(tenantid.System, "system"); !errors.As(err, &unsupported) {
		t.Fatalf("KVService on a server without a registry = %v, want *service.CapabilityUnsupportedError", err)
	}

	reg := server.NewDatabaseServiceRegistry("system")
	reg.Register("system", &server.DatabaseServices{Name: "system"})
	first := server.New(&config.Config{}, nil, nil, server.WithDatabaseServiceRegistry(reg))
	n.srv.Store(first)
	a, err := r.current()
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	b, _ := r.current()
	if a != b {
		t.Fatal("the registry was rebuilt for the same server")
	}
	second := server.New(&config.Config{}, nil, nil, server.WithDatabaseServiceRegistry(reg))
	n.srv.Store(second)
	if c, _ := r.current(); c == a {
		t.Fatal("the registry was not rebuilt for a replaced server")
	}
}

// The default database cannot be dropped through the embedded client, with
// the same typed refusal every network transport returns.
func TestNode_EmbeddedDropOfTheDefaultDatabaseIsRefused(t *testing.T) {
	n := newStandaloneNodeForTX(t)
	dbc := n.Client().Database()
	require.NotNil(t, dbc)

	err := dbc.DropDatabase(context.Background(), tenant.DefaultDatabase)
	var refused *database.DefaultDatabaseDropError
	require.True(t, errors.As(err, &refused), "DropDatabase(%q) error = %v (%T)", tenant.DefaultDatabase, err, err)
}
