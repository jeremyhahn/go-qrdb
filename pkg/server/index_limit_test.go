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
	"strconv"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// newIndexLimitServices builds the data-plane services over a memory MVCC
// store with index config indexCfg.
func newIndexLimitServices(t *testing.T, indexCfg qrdbconfig.IndexConfig) *DatabaseServices {
	t.Helper()
	eng, err := engine.NewMVCCEngine(memory.NewStorage())
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	return buildDataPlaneServices(host, 1, hub, qrdbconfig.ImportSessionConfig{}, indexCfg, qrdbconfig.TimeSeriesConfig{})
}

// createIndexes creates n indexes named idx-0..idx-(n-1) and returns the first
// refusal.
func createIndexes(svcs *DatabaseServices, n int) error {
	for i := 0; i < n; i++ {
		if err := svcs.Index.CreateIndex(context.Background(), "idx-"+strconv.Itoa(i), "f", service.IndexConfig{}); err != nil {
			return err
		}
	}
	return nil
}

func TestBuildDataPlaneServices_MaxIndexesRefusesTheIndexPastTheCap(t *testing.T) {
	svcs := newIndexLimitServices(t, qrdbconfig.IndexConfig{MaxIndexes: 2})
	if err := createIndexes(svcs, 2); err != nil {
		t.Fatalf("indexes under the cap: %v", err)
	}
	var le *service.IndexLimitError
	if err := svcs.Index.CreateIndex(context.Background(), "idx-extra", "f", service.IndexConfig{}); !errors.As(err, &le) {
		t.Fatalf("third index at max_indexes 2: err = %v (%T), want *service.IndexLimitError", err, err)
	}
}

func TestBuildDataPlaneServices_ZeroIndexConfigLeavesIndexesUnlimited(t *testing.T) {
	svcs := newIndexLimitServices(t, qrdbconfig.IndexConfig{})
	if err := createIndexes(svcs, 8); err != nil {
		t.Fatalf("max_indexes 0 must not cap: %v", err)
	}
}

func TestBuildDataPlaneServices_DroppedIndexFreesItsSlot(t *testing.T) {
	svcs := newIndexLimitServices(t, qrdbconfig.IndexConfig{MaxIndexes: 1})
	if err := createIndexes(svcs, 1); err != nil {
		t.Fatalf("first index: %v", err)
	}
	if err := svcs.Index.DropIndex(context.Background(), "idx-0"); err != nil {
		t.Fatalf("DropIndex: %v", err)
	}
	if err := svcs.Index.CreateIndex(context.Background(), "idx-next", "f", service.IndexConfig{}); err != nil {
		t.Fatalf("index after the first was dropped: %v", err)
	}
}

func TestWithTenantHostIndexConfig_SetsTheRegistryCap(t *testing.T) {
	r := &TenantHostRegistry{}
	WithTenantHostIndexConfig(qrdbconfig.IndexConfig{MaxIndexes: 5})(r)
	if r.indexCfg.MaxIndexes != 5 {
		t.Fatalf("indexCfg.MaxIndexes = %d, want 5", r.indexCfg.MaxIndexes)
	}
}

func TestWithTenantHostIndexConfig_ZeroLeavesTheRegistryUnlimited(t *testing.T) {
	r := &TenantHostRegistry{indexCfg: qrdbconfig.IndexConfig{MaxIndexes: 5}}
	WithTenantHostIndexConfig(qrdbconfig.IndexConfig{})(r)
	if r.indexCfg.MaxIndexes != 0 {
		t.Fatalf("indexCfg.MaxIndexes = %d, want 0", r.indexCfg.MaxIndexes)
	}
}

func TestWithIndexConfig_SetsTheFactoryCap(t *testing.T) {
	f := &RaftBackendFactory{}
	WithIndexConfig(qrdbconfig.IndexConfig{MaxIndexes: 7})(f)
	if f.indexCfg.MaxIndexes != 7 {
		t.Fatalf("indexCfg.MaxIndexes = %d, want 7", f.indexCfg.MaxIndexes)
	}
}

func TestWithIndexConfig_ZeroLeavesTheFactoryUnlimited(t *testing.T) {
	f := &RaftBackendFactory{indexCfg: qrdbconfig.IndexConfig{MaxIndexes: 7}}
	WithIndexConfig(qrdbconfig.IndexConfig{})(f)
	if f.indexCfg.MaxIndexes != 0 {
		t.Fatalf("indexCfg.MaxIndexes = %d, want 0", f.indexCfg.MaxIndexes)
	}
}
