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

package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/pkg/volume/volumetest"
)

// providerCatalog is a volume.Service whose GetPool answers from providers
// (pool name -> catalog provider) and refuses a missing pool as the catalog
// does. CheckProviderConsistency calls nothing else.
func providerCatalog(t *testing.T, providers map[string]string) *volumetest.Service {
	return &volumetest.Service{T: t, GetPoolFn: func(_ context.Context, name string) (*volume.PoolDetail, error) {
		provider, ok := providers[name]
		if !ok {
			return nil, &volume.PoolNotFoundError{Pool: name}
		}
		return &volume.PoolDetail{PoolView: volume.PoolView{Pool: volume.Pool{Name: name, Provider: provider}}}, nil
	}}
}

func openRealDirVolume(t *testing.T, pool string) volume.Volume {
	t.Helper()
	v, err := volume.NewDirProvider().Open(context.Background(), volume.Spec{Pool: pool, Path: t.TempDir()})
	if err != nil {
		t.Fatalf("Open(%s): %v", pool, err)
	}
	return v
}

// TestCheckProviderConsistency_MismatchRefusesJustThatPool proves security
// review finding 7: a pool this node opened with one provider, but the
// catalog recorded under a DIFFERENT provider, is refused (closed and
// removed from OpenVolumes) with a typed *volume.ProviderMismatchError --
// without touching any other pool.
func TestCheckProviderConsistency_MismatchRefusesJustThatPool(t *testing.T) {
	open := volume.NewOpenVolumes("node-1")
	mismatchedVol := openRealDirVolume(t, "fast")
	okVol := openRealDirVolume(t, "bulk")
	if err := open.Add("fast", mismatchedVol); err != nil {
		t.Fatalf("Add(fast): %v", err)
	}
	if err := open.Add("bulk", okVol); err != nil {
		t.Fatalf("Add(bulk): %v", err)
	}
	rows := []OpenedVolumePool{
		{Name: "fast", Provider: "dir", Vol: mismatchedVol},
		{Name: "bulk", Provider: "dir", Vol: okVol},
	}
	catalog := providerCatalog(t, map[string]string{
		"fast": "luks2", // the catalog says luks2; this node opened it as "dir"
		"bulk": "dir",   // matches
	})

	errs := CheckProviderConsistency(context.Background(), catalog, open, rows)
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly 1", errs)
	}
	var target *volume.ProviderMismatchError
	if !errors.As(errs[0], &target) {
		t.Fatalf("errs[0] = %v, want *volume.ProviderMismatchError", errs[0])
	}
	if target.Pool != "fast" || target.CatalogProvider != "luks2" || target.ConfiguredProvider != "dir" {
		t.Fatalf("ProviderMismatchError = %+v, want Pool=fast CatalogProvider=luks2 ConfiguredProvider=dir", target)
	}

	if _, ok := open.Get("fast"); ok {
		t.Fatal("the mismatched pool must be removed from OpenVolumes")
	}
	if _, ok := open.Get("bulk"); !ok {
		t.Fatal("the matching pool must be left alone")
	}
}

// TestCheckProviderConsistency_NoMismatchIsANoOp proves the ordinary case
// (every locally opened pool's provider matches the catalog's record)
// returns no errors and leaves every pool open.
func TestCheckProviderConsistency_NoMismatchIsANoOp(t *testing.T) {
	open := volume.NewOpenVolumes("node-1")
	vol := openRealDirVolume(t, "fast")
	if err := open.Add("fast", vol); err != nil {
		t.Fatalf("Add: %v", err)
	}
	rows := []OpenedVolumePool{{Name: "fast", Provider: "dir", Vol: vol}}
	catalog := providerCatalog(t, map[string]string{"fast": "dir"})

	errs := CheckProviderConsistency(context.Background(), catalog, open, rows)
	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
	if _, ok := open.Get("fast"); !ok {
		t.Fatal("a matching pool must stay open")
	}
}

// TestCheckProviderConsistency_SkipsPoolsThatFailedToOpenOrHaveNoCatalogRecord
// proves this check only ever concerns a pool this node actually has open
// (Vol != nil) AND that the catalog still has a record for -- a pool that
// failed to open, or that the catalog no longer lists, is some other
// mechanism's concern, not this one's.
func TestCheckProviderConsistency_SkipsPoolsThatFailedToOpenOrHaveNoCatalogRecord(t *testing.T) {
	open := volume.NewOpenVolumes("node-1")
	rows := []OpenedVolumePool{
		{Name: "broken", Provider: "dir", Err: errors.New("could not open")},
		{Name: "unknown-to-catalog", Provider: "dir", Vol: openRealDirVolume(t, "unknown-to-catalog")},
	}
	if err := open.Add("unknown-to-catalog", rows[1].Vol); err != nil {
		t.Fatalf("Add: %v", err)
	}
	catalog := providerCatalog(t, map[string]string{})

	errs := CheckProviderConsistency(context.Background(), catalog, open, rows)
	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
	if _, ok := open.Get("unknown-to-catalog"); !ok {
		t.Fatal("a pool the catalog has no record for must be left alone, not refused")
	}
}
