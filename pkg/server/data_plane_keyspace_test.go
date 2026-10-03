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
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// The data-plane services of a store many databases share refuse the empty
// compaction prefix -- it would be every database at once -- and those of a
// dedicated raft group, which is one database's whole keyspace, take it.
func TestBuildDataPlaneServices_EmptyPrefixOnlyForADedicatedKeyspace(t *testing.T) {
	eng, err := engine.NewMVCCEngine(memory.NewStorage())
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	ctx := context.Background()

	var unsafe *service.CompactPrefixInvalidError
	shared := buildDataPlaneServices(host, 1, hub).MVCC.(service.PrefixCompactor)
	if err := shared.CompactPrefix(ctx, "", 1); !errors.As(err, &unsafe) {
		t.Fatalf("a shared store's CompactPrefix(\"\") = %v, want *service.CompactPrefixInvalidError", err)
	}

	dedicated := buildDataPlaneServices(host, 1, hub, service.WithDedicatedKeyspace()).MVCC.(service.PrefixCompactor)
	if err := dedicated.CompactPrefix(ctx, "", 1); errors.As(err, &unsafe) {
		t.Fatalf("a dedicated store refused the empty prefix: %v", err)
	}
}
