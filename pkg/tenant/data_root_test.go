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

package tenant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// raftAndVolumeEntitlements permits prefix, volume, and raft isolation plus
// the dedicated-storage features each requires, for WithDataRoot tests that
// exercise both dedicated-isolation types.
func raftAndVolumeEntitlements() *quota.Entitlements {
	return &quota.Entitlements{
		AllowedIsolationModes: []string{
			quota.IsolationModePrefix,
			quota.IsolationModeVolume,
			quota.IsolationModeRaft,
		},
		AllowVolumeStorage: true,
		AllowDedicatedRaft: true,
	}
}

// newDataRootService builds a TenantService whose default entitlements permit
// both volume and raft isolation, backed by a pooled barrier registry.
func newDataRootService(t *testing.T) *TenantService {
	t.Helper()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(seal.NewMemoryBackend(), reg, testLogger(),
		WithDefaultEntitlements(raftAndVolumeEntitlements()))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	return svc
}

// newDataRootServiceOpts builds a volume/raft-entitled TenantService and
// returns it alongside its barrier registry, threading any extra options
// (e.g. WithReservedDataRoots) after the default entitlements.
func newDataRootServiceOpts(t *testing.T, opts ...TenantServiceOption) (*TenantService, *seal.BarrierRegistry) {
	t.Helper()
	reg := newTestBarrierRegistry(t)
	all := append([]TenantServiceOption{WithDefaultEntitlements(raftAndVolumeEntitlements())}, opts...)
	svc, err := NewTenantService(seal.NewMemoryBackend(), reg, testLogger(), all...)
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	return svc, reg
}

// TestDataRootsOverlap covers the component-aware containment helper directly,
// including the /data/foo vs /data/foo2 guard that a naive strings.HasPrefix
// would get wrong.
func TestDataRootsOverlap(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"equal", "/data/foo", "/data/foo", true},
		{"equal trailing slash normalized", "/data/foo/", "/data/foo", true},
		{"a parent of b", "/data/foo", "/data/foo/bar", true},
		{"b parent of a", "/data/foo/bar", "/data/foo", true},
		{"deep child", "/data", "/data/a/b/c", true},
		{"sibling foo vs foo2", "/data/foo", "/data/foo2", false},
		{"sibling foo2 vs foo", "/data/foo2", "/data/foo", false},
		{"unrelated", "/data/foo", "/other/bar", false},
		{"prefix-string but not path", "/data/foobar", "/data/foo", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dataRootsOverlap(tt.a, tt.b); got != tt.want {
				t.Errorf("dataRootsOverlap(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestCreateTenant_WithDataRoot_RejectsPoolOverlap verifies a dedicated
// DataRoot equal to, or nested under, a volume-pool base path is rejected.
func TestCreateTenant_WithDataRoot_RejectsPoolOverlap(t *testing.T) {
	svc, reg := newDataRootServiceOpts(t)
	ctx := context.Background()
	bases, err := reg.PoolBasePaths(ctx)
	if err != nil {
		t.Fatalf("PoolBasePaths: %v", err)
	}
	if len(bases) == 0 {
		t.Fatal("expected at least one pool base path")
	}
	poolBase := bases[0]

	for name, root := range map[string]string{
		"equal": poolBase,
		"child": filepath.Join(poolBase, "nested", "vt"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.CreateTenant(ctx, "vt-"+name, "VT", IsolationVolume, WithDataRoot(root))
			var overlap *DataRootOverlapError
			if !errors.As(err, &overlap) {
				t.Fatalf("CreateTenant(pool-overlap %s) = %v, want *DataRootOverlapError", name, err)
			}
			if overlap.Kind != "volume pool" {
				t.Errorf("Kind = %q, want %q", overlap.Kind, "volume pool")
			}
		})
	}
}

// TestCreateTenant_WithDataRoot_RejectsSystemDataDirOverlap verifies a
// dedicated DataRoot that equals, contains, or nests under a reserved root
// (the daemon system data dir, injected via WithReservedDataRoots) is
// rejected — covering the equal, parent, and child directions.
func TestCreateTenant_WithDataRoot_RejectsSystemDataDirOverlap(t *testing.T) {
	sysDir := t.TempDir()
	svc, _ := newDataRootServiceOpts(t, WithReservedDataRoots(sysDir))
	ctx := context.Background()

	cases := map[string]string{
		"equal": sysDir,
		"child": filepath.Join(sysDir, "nested", "vt"),
		// parent direction: DataRoot is an empty dir that is an ancestor of the
		// reserved path. The reserved child need not exist on disk.
		"parent": func() string {
			parent := t.TempDir()
			svc.reservedDataRoots = append(svc.reservedDataRoots, filepath.Join(parent, "reserved-child"))
			return parent
		}(),
	}
	i := 0
	for name, root := range cases {
		i++
		t.Run(name, func(t *testing.T) {
			_, err := svc.CreateTenant(ctx, "sd-"+name, "SD", IsolationVolume, WithDataRoot(root))
			var overlap *DataRootOverlapError
			if !errors.As(err, &overlap) {
				t.Fatalf("CreateTenant(sysdir-overlap %s) = %v, want *DataRootOverlapError", name, err)
			}
			if overlap.Kind != "system data dir" {
				t.Errorf("Kind = %q, want %q", overlap.Kind, "system data dir")
			}
		})
	}
}

// TestCreateTenant_WithDataRoot_RejectsSiblingTenantOverlap verifies a
// dedicated DataRoot nested under an existing tenant's DataRoot is rejected.
func TestCreateTenant_WithDataRoot_RejectsSiblingTenantOverlap(t *testing.T) {
	svc, _ := newDataRootServiceOpts(t)
	ctx := context.Background()
	diskA := filepath.Join(t.TempDir(), "disk", "tenant-a")
	if _, err := svc.CreateTenant(ctx, "ta", "Tenant A", IsolationVolume, WithDataRoot(diskA)); err != nil {
		t.Fatalf("CreateTenant(A): %v", err)
	}

	childOfA := filepath.Join(diskA, "nested", "tb")
	_, err := svc.CreateTenant(ctx, "tb", "Tenant B", IsolationVolume, WithDataRoot(childOfA))
	var overlap *DataRootOverlapError
	if !errors.As(err, &overlap) {
		t.Fatalf("CreateTenant(B nested in A) = %v, want *DataRootOverlapError", err)
	}
	if overlap.Kind != "tenant ta" {
		t.Errorf("Kind = %q, want %q", overlap.Kind, "tenant ta")
	}
}

// TestCreateTenant_WithDataRoot_AcceptsSeparatePath verifies a genuinely
// separate dedicated DataRoot is accepted, and that two sibling tenant roots
// /base/foo and /base/foo2 do NOT falsely overlap (the naive-prefix guard).
func TestCreateTenant_WithDataRoot_AcceptsSeparatePath(t *testing.T) {
	svc, _ := newDataRootServiceOpts(t)
	ctx := context.Background()
	base := t.TempDir()
	diskFoo := filepath.Join(base, "foo")
	diskFoo2 := filepath.Join(base, "foo2")

	if _, err := svc.CreateTenant(ctx, "tfoo", "Foo", IsolationVolume, WithDataRoot(diskFoo)); err != nil {
		t.Fatalf("CreateTenant(foo): %v", err)
	}
	// foo2 is a sibling of foo, not nested — must be accepted despite the
	// shared "/base/foo" string prefix.
	info, err := svc.CreateTenant(ctx, "tfoo2", "Foo2", IsolationVolume, WithDataRoot(diskFoo2))
	if err != nil {
		t.Fatalf("CreateTenant(foo2) = %v, want success (foo2 is not nested under foo)", err)
	}
	if info.DataRoot != diskFoo2 {
		t.Errorf("DataRoot = %q, want %q", info.DataRoot, diskFoo2)
	}
}

// TestValidateExplicitDataRoot covers validateExplicitDataRoot's acceptance
// and rejection paths directly, independent of CreateTenant.
func TestValidateExplicitDataRoot(t *testing.T) {
	dirWithFile := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirWithFile, "leftover"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed leftover file: %v", err)
	}
	emptyExistingDir := t.TempDir()
	freshPath := filepath.Join(t.TempDir(), "not-yet-created")

	tests := []struct {
		name    string
		path    string
		wantErr error // sentinel to match with errors.Is, nil if only type matters
		wantAs  any   // pointer-to-pointer for errors.As, nil to skip
	}{
		{name: "empty", path: "", wantErr: ErrEmptyDataRoot},
		{name: "relative", path: "relative/path", wantAs: new(*InvalidDataRootError)},
		{name: "non-canonical double separator", path: "/tmp//double", wantAs: new(*InvalidDataRootError)},
		{name: "non-canonical dot-dot", path: "/tmp/a/../b", wantAs: new(*InvalidDataRootError)},
		{name: "existing non-empty dir", path: dirWithFile, wantAs: new(*DataRootNotEmptyError)},
		{name: "existing empty dir ok", path: emptyExistingDir, wantErr: nil},
		{name: "not-yet-existing path ok", path: freshPath, wantErr: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateExplicitDataRoot(tt.path)
			if tt.wantErr == nil && tt.wantAs == nil {
				if err != nil {
					t.Fatalf("validateExplicitDataRoot(%q) = %v, want nil", tt.path, err)
				}
				return
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("validateExplicitDataRoot(%q) = %v, want errors.Is match for %v", tt.path, err, tt.wantErr)
				}
				return
			}
			if !errors.As(err, tt.wantAs) {
				t.Fatalf("validateExplicitDataRoot(%q) = %v (%T), want errors.As match for %T", tt.path, err, err, tt.wantAs)
			}
		})
	}
}

// TestValidateExplicitDataRoot_RejectsSymlink verifies that a canonical path
// which nonetheless traverses a symlink is rejected — the classic escape the
// filepath.Clean canonical check cannot see.
func TestValidateExplicitDataRoot_RejectsSymlink(t *testing.T) {
	// Resolve the tmp base first so the test is robust on systems where the
	// tempdir itself is symlinked (e.g. macOS /var -> /private/var); only the
	// symlink we introduce below should trip the check.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve tmp base: %v", err)
	}
	target := filepath.Join(base, "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	var invalid *InvalidDataRootError
	if err := validateExplicitDataRoot(link); !errors.As(err, &invalid) {
		t.Fatalf("validateExplicitDataRoot(symlink) = %v (%T), want *InvalidDataRootError", err, err)
	}
	// A not-yet-existing leaf under a symlinked ancestor is rejected too.
	nested := filepath.Join(link, "child")
	if err := validateExplicitDataRoot(nested); !errors.As(err, &invalid) {
		t.Fatalf("validateExplicitDataRoot(symlinked ancestor) = %v, want *InvalidDataRootError", err)
	}
	// The real (non-symlinked) empty target still validates cleanly.
	if err := validateExplicitDataRoot(target); err != nil {
		t.Fatalf("validateExplicitDataRoot(real dir) = %v, want nil", err)
	}
}

// TestCreateTenant_WithDataRoot_Volume verifies that WithDataRoot pins an
// IsolationVolume tenant's DataRoot to the caller-supplied path instead of
// pool allocation, and that the barrier's on-disk state is actually created
// under it.
func TestCreateTenant_WithDataRoot_Volume(t *testing.T) {
	svc := newDataRootService(t)
	ctx := context.Background()
	dataRoot := filepath.Join(t.TempDir(), "dedicated-disk", "vtenant")

	info, err := svc.CreateTenant(ctx, "vtenant", "Volume Tenant", IsolationVolume, WithDataRoot(dataRoot))
	if err != nil {
		t.Fatalf("CreateTenant(volume, WithDataRoot): %v", err)
	}
	if info.DataRoot != dataRoot {
		t.Fatalf("DataRoot = %q, want %q", info.DataRoot, dataRoot)
	}
	if _, statErr := os.Stat(filepath.Join(dataRoot, "barrier")); statErr != nil {
		t.Fatalf("barrier subdirectory not created under explicit DataRoot: %v", statErr)
	}
}

// TestCreateTenant_WithDataRoot_Raft verifies that WithDataRoot pins an
// IsolationRaft tenant's DataRoot the same way as IsolationVolume.
func TestCreateTenant_WithDataRoot_Raft(t *testing.T) {
	svc := newDataRootService(t)
	ctx := context.Background()
	dataRoot := filepath.Join(t.TempDir(), "dedicated-disk", "rtenant")

	info, err := svc.CreateTenant(ctx, "rtenant", "Raft Tenant", IsolationRaft, WithDataRoot(dataRoot))
	if err != nil {
		t.Fatalf("CreateTenant(raft, WithDataRoot): %v", err)
	}
	if info.DataRoot != dataRoot {
		t.Fatalf("DataRoot = %q, want %q", info.DataRoot, dataRoot)
	}
	if _, statErr := os.Stat(filepath.Join(dataRoot, "barrier")); statErr != nil {
		t.Fatalf("barrier subdirectory not created under explicit DataRoot: %v", statErr)
	}
}

// TestCreateTenant_WithDataRoot_RejectedForPrefixIsolation verifies that
// WithDataRoot is rejected for the default (prefix) isolation type, which has
// no dedicated filesystem DataRoot.
func TestCreateTenant_WithDataRoot_RejectedForPrefixIsolation(t *testing.T) {
	svc := newDataRootService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "ptenant", "Prefix Tenant", IsolationPrefix, WithDataRoot(t.TempDir()))
	var dite *DataRootIsolationTypeError
	if !errors.As(err, &dite) {
		t.Fatalf("CreateTenant(prefix, WithDataRoot) = %v, want *DataRootIsolationTypeError", err)
	}
}

// TestCreateTenant_WithDataRoot_RejectedForBarrierIsolation verifies that
// WithDataRoot is rejected for IsolationBarrier, which has no dedicated
// filesystem DataRoot (its barrier occupies the whole per-tenant pool slot).
func TestCreateTenant_WithDataRoot_RejectedForBarrierIsolation(t *testing.T) {
	svc := newDataRootService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "btenant", "Barrier Tenant", IsolationBarrier, WithDataRoot(t.TempDir()))
	var dite *DataRootIsolationTypeError
	if !errors.As(err, &dite) {
		t.Fatalf("CreateTenant(barrier, WithDataRoot) = %v, want *DataRootIsolationTypeError", err)
	}
}

// TestCreateTenant_WithDataRoot_RejectsNonEmptyDir verifies that CreateTenant
// fails closed rather than adopting another tenant's (or leftover) data when
// the requested DataRoot already contains files.
func TestCreateTenant_WithDataRoot_RejectsNonEmptyDir(t *testing.T) {
	svc := newDataRootService(t)
	ctx := context.Background()
	dataRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataRoot, "leftover"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed leftover file: %v", err)
	}

	_, err := svc.CreateTenant(ctx, "vtenant2", "Volume Tenant 2", IsolationVolume, WithDataRoot(dataRoot))
	var dne *DataRootNotEmptyError
	if !errors.As(err, &dne) {
		t.Fatalf("CreateTenant(volume, non-empty DataRoot) = %v, want *DataRootNotEmptyError", err)
	}
}

// TestCreateTenant_WithDataRoot_RejectsRelativePath verifies that CreateTenant
// rejects a relative WithDataRoot path before any registration side effect.
func TestCreateTenant_WithDataRoot_RejectsRelativePath(t *testing.T) {
	svc := newDataRootService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "vtenant3", "Volume Tenant 3", IsolationVolume, WithDataRoot("relative/path"))
	var idre *InvalidDataRootError
	if !errors.As(err, &idre) {
		t.Fatalf("CreateTenant(volume, relative DataRoot) = %v, want *InvalidDataRootError", err)
	}
}
