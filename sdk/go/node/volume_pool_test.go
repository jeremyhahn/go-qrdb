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
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/pkg/volume/volumetest"
)

func TestOpenConfiguredVolumes_RecordsEveryFailure(t *testing.T) {
	ctx := context.Background()
	registry, err := newVolumeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dup := t.TempDir()
	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "unknown", Provider: "no-such-provider", Path: t.TempDir()},
		{Name: "file", Path: file},
		{Name: "dup", Path: dup},
		{Name: "dup", Path: dup},
	}
	open, rows := openConfiguredVolumes(ctx, "node-1", pools, registry, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { closeOpenedVolumes(ctx, rows, slog.New(slog.DiscardHandler)) })
	if len(rows) != len(pools) {
		t.Fatalf("rows = %d, want %d", len(rows), len(pools))
	}
	for i, want := range []struct {
		provider string
		opened   bool
	}{{"no-such-provider", false}, {"dir", false}, {"dir", true}, {"dir", false}} {
		row := rows[i]
		if row.provider != want.provider || (row.vol != nil) != want.opened || (row.err == nil) != want.opened {
			t.Errorf("row %d (%s) = provider %q, vol %v, err %v; want provider %q, opened %v", i, row.name, row.provider, row.vol, row.err, want.provider, want.opened)
		}
	}
	if got := open.Pools(); len(got) != 1 || got[0] != "dup" {
		t.Errorf("open pools = %v, want only the first dup", got)
	}
}

func TestCheckProviderConsistency_EvictsAMismatchedPool(t *testing.T) {
	ctx := context.Background()
	errMissing := errors.New("not in the catalog")
	catalog := &volumetest.Service{T: t, GetPoolFn: func(_ context.Context, name string) (*volume.PoolDetail, error) {
		if name == "uncataloged" {
			return nil, errMissing
		}
		return &volume.PoolDetail{PoolView: volume.PoolView{Provider: "lvm"}}, nil
	}}
	open := volume.NewOpenVolumes("node-1")
	matching, uncataloged, mismatched := &stubVolume{root: t.TempDir()}, &stubVolume{root: t.TempDir()}, &stubVolume{root: t.TempDir()}
	for name, v := range map[string]volume.Volume{"matching": matching, "uncataloged": uncataloged, "mismatched": mismatched} {
		if err := open.Add(name, v); err != nil {
			t.Fatal(err)
		}
	}
	rows := []openedVolumePool{
		{name: "failed", provider: "dir", err: errMissing},
		{name: "uncataloged", provider: "dir", vol: uncataloged},
		{name: "matching", provider: "lvm", vol: matching},
		{name: "mismatched", provider: "dir", vol: mismatched},
	}
	errs := checkProviderConsistency(ctx, catalog, open, rows)
	var mismatch *volume.ProviderMismatchError
	if len(errs) != 1 || !errors.As(errs[0], &mismatch) || mismatch.Pool != "mismatched" || mismatch.CatalogProvider != "lvm" || mismatch.ConfiguredProvider != "dir" {
		t.Fatalf("errs = %v; want one *volume.ProviderMismatchError for mismatched", errs)
	}
	if !mismatched.closed || matching.closed || uncataloged.closed {
		t.Errorf("closed: mismatched %v, matching %v, uncataloged %v; want only mismatched", mismatched.closed, matching.closed, uncataloged.closed)
	}
	if got := open.Pools(); len(got) != 2 {
		t.Errorf("open pools after the check = %v, want matching and uncataloged", got)
	}
}

func TestCloseOpenedVolumes_LogsACloseFailure(t *testing.T) {
	var logs bytes.Buffer
	failing := &stubVolume{closeErr: errors.New("device busy")}
	closing := &stubVolume{}
	closeOpenedVolumes(context.Background(), []openedVolumePool{
		{name: "unopened", err: errors.New("never opened")},
		{name: "busy", vol: failing},
		{name: "fine", vol: closing},
	}, slog.New(slog.NewTextHandler(&logs, nil)))
	if !failing.closed || !closing.closed {
		t.Fatal("an opened volume was not closed")
	}
	if out := logs.String(); !strings.Contains(out, "failed to close volume") || !strings.Contains(out, "pool=busy") || strings.Contains(out, "pool=fine") {
		t.Errorf("logs = %q; want one close failure, for busy", out)
	}
}

func TestBuildNodeReport(t *testing.T) {
	errOpen := errors.New("device not present")
	errUsage := errors.New("statfs failed")
	report := buildNodeReport(context.Background(), "node-1", []openedVolumePool{
		{name: "down", err: errOpen},
		{name: "unmeasured", vol: &stubVolume{usageErr: errUsage, health: volume.Health{State: volume.StateReady}}},
		{name: "explained", vol: &stubVolume{usageErr: errUsage, health: volume.Health{State: volume.StateLocked, Detail: "locked"}}},
	})
	if report.NodeID != "node-1" || len(report.Pools) != 3 {
		t.Fatalf("report = %+v", report)
	}
	if down := report.Pools["down"]; !down.Mapped || down.Health.State != volume.StateUnavailable || down.Health.Detail != errOpen.Error() {
		t.Errorf("down = %+v", down)
	}
	if got := report.Pools["unmeasured"].Health; got.State != volume.StateReady || got.Detail != errUsage.Error() {
		t.Errorf("unmeasured health = %+v; want the usage failure as its detail", got)
	}
	if got := report.Pools["explained"].Health; got.Detail != "locked" {
		t.Errorf("explained health = %+v; a provider detail must not be overwritten", got)
	}
}

func TestNewVolumeRegistry_RefusesANilProvider(t *testing.T) {
	registry, err := newVolumeRegistry(volume.Provider(nil))
	var invalid *volume.InvalidProviderError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v; want *volume.InvalidProviderError", err)
	}
	if registry != nil {
		t.Fatal("a refused build must return no registry")
	}
}
