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
	"log/slog"
	"strings"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// fakeVolumeProvider is a minimal volume.Provider double, for proving
// NewVolumeRegistry registers extra providers (the WithVolumeProvider
// seam) without needing a real "dir"-shaped backend.
type fakeVolumeProvider struct{ name string }

func (p *fakeVolumeProvider) Name() string { return p.name }
func (p *fakeVolumeProvider) Open(context.Context, volume.Spec) (volume.Volume, error) {
	return nil, nil
}

func TestNewVolumeRegistry_RegistersExtraProviders(t *testing.T) {
	registry, err := NewVolumeRegistry(&fakeVolumeProvider{name: "luks2"})
	if err != nil {
		t.Fatalf("NewVolumeRegistry: %v", err)
	}
	for _, want := range []string{"dir", "luks2"} {
		if _, err := registry.Lookup(want); err != nil {
			t.Fatalf("Lookup(%s): %v", want, err)
		}
	}
}

func TestNewVolumeRegistry_DuplicateNameRefused(t *testing.T) {
	_, err := NewVolumeRegistry(&fakeVolumeProvider{name: "dir"})
	if err == nil {
		t.Fatal("NewVolumeRegistry(duplicate \"dir\") should fail")
	}
}

// TestOpenConfiguredVolumes_UnregisteredProviderReportsAndContinues proves
// a pool naming a provider this node has not registered is reported
// (Mapped=true, err set) rather than aborting the other pools.
func TestOpenConfiguredVolumes_UnregisteredProviderReportsAndContinues(t *testing.T) {
	registry, err := NewVolumeRegistry()
	if err != nil {
		t.Fatalf("NewVolumeRegistry: %v", err)
	}
	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "missing", Provider: "luks2", Path: t.TempDir()},
		{Name: "present", Provider: "dir", Path: t.TempDir()},
	}
	open, rows := OpenConfiguredVolumes(context.Background(), "node-1", pools, registry, slog.Default())
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Vol != nil || rows[0].Err == nil {
		t.Fatalf("rows[0] (missing provider) = %+v, want Vol=nil, Err set", rows[0])
	}
	if rows[1].Vol == nil || rows[1].Err != nil {
		t.Fatalf("rows[1] (dir) = %+v, want Vol set, Err=nil", rows[1])
	}
	if _, ok := open.Get("present"); !ok {
		t.Fatal("OpenVolumes should track the pool that opened successfully")
	}
	CloseOpenedVolumes(context.Background(), rows, slog.Default())
}

// TestBuildNodeReport_NeverCarriesAFilesystemPath proves the security
// review's invariant: a NodeReport (replicated, wire-facing) never
// contains this node's own filesystem path, for either an open failure or
// a Volume whose Usage query failed. The failing path itself is asserted
// absent from BOTH Detail strings.
func TestBuildNodeReport_NeverCarriesAFilesystemPath(t *testing.T) {
	registry, err := NewVolumeRegistry()
	if err != nil {
		t.Fatalf("NewVolumeRegistry: %v", err)
	}
	secretPath := t.TempDir() + "/nested/does-not-exist"
	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "broken", Provider: "dir", Path: secretPath},
	}
	_, rows := OpenConfiguredVolumes(context.Background(), "node-1", pools, registry, slog.Default())
	report := BuildNodeReport(context.Background(), "node-1", rows, slog.Default())

	row, ok := report.Pools["broken"]
	if !ok {
		t.Fatal("report missing the \"broken\" pool")
	}
	if row.Health.State != volume.StateUnavailable {
		t.Fatalf("Health.State = %v, want StateUnavailable", row.Health.State)
	}
	if strings.Contains(row.Health.Detail, secretPath) {
		t.Fatalf("Health.Detail = %q, must never contain the node's filesystem path %q", row.Health.Detail, secretPath)
	}
}

// TestBuildNodeReport_SuccessReportsUsageAndHealth proves the ordinary,
// successful case: a pool this node opened without error reports its
// live Usage and Health, Mapped=true.
func TestBuildNodeReport_SuccessReportsUsageAndHealth(t *testing.T) {
	registry, err := NewVolumeRegistry()
	if err != nil {
		t.Fatalf("NewVolumeRegistry: %v", err)
	}
	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "fast", Provider: "dir", Path: t.TempDir()},
	}
	_, rows := OpenConfiguredVolumes(context.Background(), "node-1", pools, registry, slog.Default())
	report := BuildNodeReport(context.Background(), "node-1", rows, slog.Default())

	row, ok := report.Pools["fast"]
	if !ok {
		t.Fatal("report missing the \"fast\" pool")
	}
	if !row.Mapped {
		t.Fatal("Mapped = false, want true for a pool this node's config maps")
	}
	if row.Health.State != volume.StateReady {
		t.Fatalf("Health.State = %v, want StateReady", row.Health.State)
	}
}

// fakeUsageFailingVolume is a volume.Volume double whose Usage always
// fails, for BuildNodeReport's "usage query failed" branch -- a real
// provider's Usage() can fail independently of Health() (e.g. a statfs
// race against a just-unmounted device) even when the volume itself
// reports healthy.
type fakeUsageFailingVolume struct {
	health volume.Health
}

func (f *fakeUsageFailingVolume) Root() string { return "/fake" }
func (f *fakeUsageFailingVolume) Usage(context.Context) (volume.Usage, error) {
	return volume.Usage{}, errors.New("statfs: device busy")
}
func (f *fakeUsageFailingVolume) Health(context.Context) volume.Health { return f.health }
func (f *fakeUsageFailingVolume) Close(context.Context) error          { return nil }

// TestBuildNodeReport_UsageFailureGetsAGenericDetailWhenHealthHasNone
// proves the "usage query failed" branch: when Health.Detail is already
// empty, BuildNodeReport fills it with a generic, cause-only string (not
// the raw usage error, which could embed a path) rather than leaving it
// blank.
func TestBuildNodeReport_UsageFailureGetsAGenericDetailWhenHealthHasNone(t *testing.T) {
	rows := []OpenedVolumePool{
		{Name: "flaky", Provider: "dir", Vol: &fakeUsageFailingVolume{health: volume.Health{State: volume.StateReady}}},
	}
	report := BuildNodeReport(context.Background(), "node-1", rows, slog.Default())

	row, ok := report.Pools["flaky"]
	if !ok {
		t.Fatal("report missing the \"flaky\" pool")
	}
	if row.Health.Detail == "" {
		t.Fatal("Health.Detail should be filled in when the usage query fails")
	}
}

// TestBuildNodeReport_UsageFailureKeepsHealthsOwnDetail proves
// BuildNodeReport never OVERWRITES a Health.Detail the Volume itself
// already set, even when the usage query also failed.
func TestBuildNodeReport_UsageFailureKeepsHealthsOwnDetail(t *testing.T) {
	rows := []OpenedVolumePool{
		{Name: "flaky", Provider: "dir", Vol: &fakeUsageFailingVolume{health: volume.Health{State: volume.StateUnavailable, Detail: "not writable"}}},
	}
	report := BuildNodeReport(context.Background(), "node-1", rows, slog.Default())

	row := report.Pools["flaky"]
	if row.Health.Detail != "not writable" {
		t.Fatalf("Health.Detail = %q, want the Volume's own \"not writable\" preserved", row.Health.Detail)
	}
}

// TestOpenConfiguredVolumes_DuplicatePoolNameReportsAndContinues proves
// the open.Add failure branch: two config entries naming the same pool
// collide on registration, reported per-row rather than aborting the
// whole sweep.
func TestOpenConfiguredVolumes_DuplicatePoolNameReportsAndContinues(t *testing.T) {
	registry, err := NewVolumeRegistry()
	if err != nil {
		t.Fatalf("NewVolumeRegistry: %v", err)
	}
	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "fast", Provider: "dir", Path: t.TempDir()},
		{Name: "fast", Provider: "dir", Path: t.TempDir()},
	}
	open, rows := OpenConfiguredVolumes(context.Background(), "node-1", pools, registry, slog.Default())
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Vol == nil || rows[0].Err != nil {
		t.Fatalf("rows[0] (first \"fast\") = %+v, want Vol set, Err nil", rows[0])
	}
	if rows[1].Vol != nil || rows[1].Err == nil {
		t.Fatalf("rows[1] (duplicate \"fast\") = %+v, want Vol nil, Err set", rows[1])
	}
	if _, ok := open.Get("fast"); !ok {
		t.Fatal("the first successful open must still be tracked")
	}
}

// fakeCloseFailingVolume is a volume.Volume double whose Close always
// fails, for CloseOpenedVolumes' error-logging branch.
type fakeCloseFailingVolume struct{}

func (fakeCloseFailingVolume) Root() string { return "/fake" }
func (fakeCloseFailingVolume) Usage(context.Context) (volume.Usage, error) {
	return volume.Usage{}, nil
}
func (fakeCloseFailingVolume) Health(context.Context) volume.Health {
	return volume.Health{State: volume.StateReady}
}
func (fakeCloseFailingVolume) Close(context.Context) error { return errors.New("close: device busy") }

// TestCloseOpenedVolumes_LogsAndContinuesOnCloseFailure proves a Close
// failure is logged (not failed on, and not returned) and every other
// row's Volume is still closed.
func TestCloseOpenedVolumes_LogsAndContinuesOnCloseFailure(t *testing.T) {
	closedOK := openRealDirVolume(t, "fast")
	rows := []OpenedVolumePool{
		{Name: "broken", Provider: "dir", Vol: fakeCloseFailingVolume{}},
		{Name: "fast", Provider: "dir", Vol: closedOK},
		{Name: "never-opened", Provider: "dir", Err: errors.New("could not open")},
	}
	// Must not panic and must not block on the never-opened (nil Vol) row.
	CloseOpenedVolumes(context.Background(), rows, slog.Default())
}

// TestFailedOpens_CollectsOnlyTheFailedRows proves FailedOpens names every
// row whose Err is set, in order, carrying the pool, the provider this
// node's own configuration selected, and the cause -- and skips rows that
// opened successfully.
func TestFailedOpens_CollectsOnlyTheFailedRows(t *testing.T) {
	boom := errors.New("boom")
	rows := []OpenedVolumePool{
		{Name: "fast", Provider: "dir", Vol: openRealDirVolume(t, "fast")},
		{Name: "vault", Provider: "luks2", Err: boom},
		{Name: "bulk", Provider: "dir", Err: errors.New("not mounted")},
	}
	failed := FailedOpens(rows)
	if len(failed) != 2 {
		t.Fatalf("len(failed) = %d, want 2", len(failed))
	}
	if failed[0].Pool != "vault" || failed[0].Provider != "luks2" || failed[0].Err != boom {
		t.Fatalf("failed[0] = %+v, want {vault luks2 boom}", failed[0])
	}
	if failed[1].Pool != "bulk" || failed[1].Provider != "dir" {
		t.Fatalf("failed[1] = %+v, want pool=bulk provider=dir", failed[1])
	}
}

// TestFailedOpens_NoFailuresIsEmpty proves FailedOpens returns an empty
// slice, not nil-vs-empty ambiguity that would trip a caller checking
// len(failed) == 0, when every row opened.
func TestFailedOpens_NoFailuresIsEmpty(t *testing.T) {
	rows := []OpenedVolumePool{
		{Name: "fast", Provider: "dir", Vol: openRealDirVolume(t, "fast")},
	}
	if failed := FailedOpens(rows); len(failed) != 0 {
		t.Fatalf("len(failed) = %d, want 0", len(failed))
	}
}
