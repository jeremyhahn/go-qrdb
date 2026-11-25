package lifecycle

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/backupsink"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

func TestOpenBackupDestination_LocalRoundTrips(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	engine, err := OpenBackupDestination(qrdbconfig.ObjstoreConfig{Provider: "local", Settings: map[string]string{"path": dir}})
	if err != nil {
		t.Fatalf("OpenBackupDestination() = %v", err)
	}
	sink := backupsink.New(engine)
	ctx := context.Background()
	info := service.BackupInfo{ID: "b-1", Timestamp: time.Unix(1, 0).UTC()}
	if err := sink.Write(ctx, info, map[uint64][]byte{1: []byte("shard")}, nil); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	list, err := sink.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "b-1" {
		t.Fatalf("List() = %v, %v, want the one backup b-1", list, err)
	}
	shards, err := sink.ReadShards(ctx, "b-1")
	if err != nil || string(shards[1]) != "shard" {
		t.Fatalf("ReadShards() = %v, %v, want shard 1", shards, err)
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

func TestOpenBackupDestination_LocalIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	engine, err := OpenBackupDestination(qrdbconfig.ObjstoreConfig{Provider: "local", Settings: map[string]string{"path": path}})
	if err != nil {
		t.Fatalf("OpenBackupDestination() = %v", err)
	}
	sink := backupsink.New(engine)
	info := service.BackupInfo{ID: "b-1", Timestamp: time.Unix(1, 0).UTC()}
	envelope := &service.BackupEnvelope{FormatVersion: 1, ProviderID: "test"}
	if err := sink.Write(context.Background(), info, map[uint64][]byte{1: []byte("shard")}, envelope); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	root, err := os.Stat(path)
	if err != nil || root.Mode().Perm() != 0o700 {
		t.Fatalf("root %s = %v, %v, want mode 0700", path, root, err)
	}
	for _, tree := range []string{path, path + ".journal"} {
		if _, err := os.Stat(tree); errors.Is(err, fs.ErrNotExist) && tree != path {
			continue
		}
		var files int
		err := filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			st, err := d.Info()
			if err != nil {
				return err
			}
			if st.Mode().Perm()&0o077 != 0 {
				t.Errorf("%s has mode %v, want no group or other access", p, st.Mode().Perm())
			}
			if !d.IsDir() {
				files++
			}
			return nil
		})
		if err != nil {
			t.Fatalf("WalkDir(%s) = %v", tree, err)
		}
		if tree == path && files == 0 {
			t.Fatalf("no object written under %s", path)
		}
	}
}

func TestOpenBackupDestination_LocalRefusesGroupAccessibleRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("Mkdir() = %v", err)
	}
	if err := os.Chmod(dir, 0o750); err != nil { // #nosec G302 -- a group-accessible root is the fault this test injects
		t.Fatalf("Chmod() = %v", err)
	}
	engine, err := OpenBackupDestination(qrdbconfig.ObjstoreConfig{Provider: "local", Settings: map[string]string{"path": dir}})
	if err == nil {
		_ = engine.Close()
		t.Fatal("OpenBackupDestination() on a group-accessible root succeeded")
	}
	var modeErr *objstore.GroupAccessibleRootError
	if !errors.As(err, &modeErr) || modeErr.Path != dir {
		t.Fatalf("OpenBackupDestination() = %v, want a GroupAccessibleRootError for %s", err, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("ReadDir(%s) = %v, %v, want nothing written", dir, entries, err)
	}
}

func TestOpenBackupDestination_UnsupportedProvider(t *testing.T) {
	_, err := OpenBackupDestination(qrdbconfig.ObjstoreConfig{Provider: "no-such-provider"})
	var de *BackupDestinationError
	if !errors.As(err, &de) || de.Provider != "no-such-provider" {
		t.Fatalf("OpenBackupDestination() = %v, want a BackupDestinationError for no-such-provider", err)
	}
	var unsupported *objstore.UnsupportedProviderError
	if !errors.As(err, &unsupported) {
		t.Fatalf("OpenBackupDestination() = %v, want it to wrap an UnsupportedProviderError", err)
	}
}

func TestOpenBackupDestination_RefusesAnEmptyProvider(t *testing.T) {
	engine, err := OpenBackupDestination(qrdbconfig.ObjstoreConfig{Settings: map[string]string{"path": t.TempDir()}})
	var de *BackupDestinationError
	if engine != nil || !errors.As(err, &de) {
		t.Fatalf("OpenBackupDestination() = %v, %v, want a BackupDestinationError", engine, err)
	}
}
