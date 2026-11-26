package seal

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// The copy-back staging name is one bounded path component: stable for one
// tenant path, distinct for two, and never longer than the 255-byte limit
// however long the tenant's own (valid) path is.
func TestCopyBackStagingName_BoundedStableDistinct(t *testing.T) {
	long := strings.Repeat("a", 255)
	for _, path := range []string{"acme", long} {
		name := copyBackStagingName(path)
		if len(name) > 255 {
			t.Fatalf("copyBackStagingName(%d bytes) is %d bytes, want <= 255", len(path), len(name))
		}
		if name != filepath.Base(name) || !strings.HasPrefix(name, volume.ReservedPathPrefix) {
			t.Fatalf("copyBackStagingName(%q) = %q, want one component under %q", path[:4], name, volume.ReservedPathPrefix)
		}
		if again := copyBackStagingName(path); again != name {
			t.Fatalf("copyBackStagingName is not stable: %q then %q", name, again)
		}
	}
	if copyBackStagingName("acme") == copyBackStagingName("acme2") {
		t.Fatalf("two tenant paths share the staging name %q", copyBackStagingName("acme"))
	}
}

// A tenant whose assignment path is a valid 250-byte component rolls back
// after its copy: the staging directory beside it fits the filesystem's
// component limit, the source ends up holding the destination's tree and
// no staging directory remains.
func TestMoveStepRunner_CopyBackLongTenantPath(t *testing.T) {
	path := strings.Repeat("t", 250)
	if err := volume.ValidateAssignmentPath(moveTestTenant, path); err != nil {
		t.Fatalf("ValidateAssignmentPath(250 bytes): %v", err)
	}
	ctx := context.Background()
	fx := newMoveFixtureAt(t, path)
	if err := fx.reg.system.Initialize(ctx, MustCredentials(ctx, []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	stores := &fakeStores{t: t, reg: fx.reg}
	if err := stores.OpenTenantStores(ctx, moveTestTenant); err != nil {
		t.Fatalf("open: %v", err)
	}
	r, err := NewMoveStepRunner(fx.reg, moveTestNode, stores)
	if err != nil {
		t.Fatalf("NewMoveStepRunner: %v", err)
	}
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := r.SettleMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("settle copy: %v", err)
	}
	stores.write("w1")
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseAbort, true)); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if got, want := mustManifest(t, fx.src), mustManifest(t, fx.dst()); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored source differs from the destination")
	}
	mustExist(t, filepath.Join(fx.src, "w1"))
	assertGone(t, filepath.Join(fx.defaultRoot, copyBackStagingName(path)))
	entries, err := os.ReadDir(fx.defaultRoot)
	if err != nil {
		t.Fatalf("ReadDir(default root): %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), volume.ReservedPathPrefix+"copyback.") {
			t.Fatalf("staging directory %q remains", e.Name())
		}
	}
}
