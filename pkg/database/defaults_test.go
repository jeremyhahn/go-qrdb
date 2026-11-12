package database

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/config"
)

func TestDefaultsFromConfig(t *testing.T) {
	got := DefaultsFromConfig(config.DatabaseDefaults{Backend: "raft", Replicas: 3,
		Raft: config.DatabaseRaftDefaults{ClusterMode: "distributed", StateStorage: "file"}})
	want := Defaults{Backend: BackendRaft, Replicas: 3, RaftClusterMode: RaftDistributed, RaftStateStorage: BackendFile}
	if got != want {
		t.Fatalf("DefaultsFromConfig = %+v, want %+v", got, want)
	}
	if got := DefaultsFromConfig(config.DatabaseDefaults{}); got != (Defaults{}) {
		t.Fatalf("DefaultsFromConfig(empty) = %+v, want zero", got)
	}
}

func TestDefaultsFill(t *testing.T) {
	d := Defaults{Backend: BackendRaft, Replicas: 3, RaftClusterMode: RaftDistributed, RaftStateStorage: BackendFile}
	tests := []struct {
		name string
		d    Defaults
		cfg  DatabaseConfig
		want DatabaseConfig
	}{
		{
			name: "empty fields filled",
			d:    d,
			cfg:  DatabaseConfig{Name: "app", Raft: &DatabaseRaftConfig{}},
			want: DatabaseConfig{Name: "app", Backend: BackendRaft, Replicas: 3,
				Raft: &DatabaseRaftConfig{ClusterMode: RaftDistributed, StateStorage: BackendFile}},
		},
		{
			name: "recorded state storage kept",
			d:    d,
			cfg:  DatabaseConfig{Name: "app", Raft: &DatabaseRaftConfig{StateStorage: BackendMemory}},
			want: DatabaseConfig{Name: "app", Backend: BackendRaft, Replicas: 3,
				Raft: &DatabaseRaftConfig{ClusterMode: RaftDistributed, StateStorage: BackendMemory}},
		},
		{
			name: "set fields kept",
			d:    d,
			cfg: DatabaseConfig{Name: "app", Backend: BackendRaft, Replicas: 5,
				Raft: &DatabaseRaftConfig{ClusterMode: RaftLocal}},
			want: DatabaseConfig{Name: "app", Backend: BackendRaft, Replicas: 5,
				Raft: &DatabaseRaftConfig{ClusterMode: RaftLocal, StateStorage: BackendFile}},
		},
		{
			name: "no raft block added",
			d:    d,
			cfg:  DatabaseConfig{Name: "app"},
			want: DatabaseConfig{Name: "app", Backend: BackendRaft, Replicas: 3},
		},
		{
			name: "raft defaults skip a non-raft backend",
			d:    d,
			cfg:  DatabaseConfig{Name: "app", Backend: BackendPebble},
			want: DatabaseConfig{Name: "app", Backend: BackendPebble},
		},
		{
			name: "no defaults leave the record empty",
			cfg:  DatabaseConfig{Name: "app"},
			want: DatabaseConfig{Name: "app"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			tc.d.fill(&cfg)
			if !reflect.DeepEqual(cfg, tc.want) {
				t.Fatalf("fill = %+v, want %+v", cfg, tc.want)
			}
		})
	}
}

func TestDatabaseRaftConfigValidateStateStorage(t *testing.T) {
	for _, s := range []BackendType{"", BackendPebble, BackendFile, BackendMemory} {
		if err := (&DatabaseRaftConfig{ClusterMode: RaftNone, StateStorage: s}).Validate(); err != nil {
			t.Fatalf("Validate(state storage %q) = %v, want nil", s, err)
		}
	}
	err := (&DatabaseRaftConfig{ClusterMode: RaftNone, StateStorage: BackendObjstore}).Validate()
	var target *InvalidRaftStateStorageError
	if !errors.As(err, &target) || target.StateStorage != BackendObjstore {
		t.Fatalf("Validate(objstore) = %v, want *InvalidRaftStateStorageError", err)
	}
}

func TestDefaultsFillDoesNotMutateCallerRaftBlock(t *testing.T) {
	raft := &DatabaseRaftConfig{}
	cfg := DatabaseConfig{Name: "app", Backend: BackendRaft, Raft: raft}
	Defaults{RaftClusterMode: RaftDistributed}.fill(&cfg)
	if raft.ClusterMode != "" {
		t.Fatalf("caller's raft block mutated: ClusterMode = %q", raft.ClusterMode)
	}
}

func TestManagerCreateRecordsDefaults(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	m := newCatalogManager(logger, dataDir, WithDefaults(Defaults{Backend: BackendPebble}))
	m.SetShardIDClaimer(testShardClaimer(t))
	filled, err := m.Create(DatabaseConfig{Name: "filled"})
	if err != nil {
		t.Fatalf("Create(filled) = %v", err)
	}
	if got := filled.Config().Backend; got != BackendPebble {
		t.Fatalf("filled backend = %q, want %q", got, BackendPebble)
	}
	kept, err := m.Create(DatabaseConfig{Name: "kept", Backend: BackendFile})
	if err != nil {
		t.Fatalf("Create(kept) = %v", err)
	}
	if got := kept.Config().Backend; got != BackendFile {
		t.Fatalf("kept backend = %q, want %q", got, BackendFile)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	// A node whose defaults changed opens the database from its record.
	reopened := newCatalogManager(logger, dataDir, WithDefaults(Defaults{Backend: BackendFile}))
	reopened.SetShardIDClaimer(testShardClaimer(t))
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.OpenAll(ctx); err != nil {
		t.Fatalf("OpenAll() = %v", err)
	}
	db, err := reopened.Open(ctx, filled.Config().TenantID, "filled")
	if err != nil {
		t.Fatalf("Open(filled) = %v", err)
	}
	if got := db.Config().Backend; got != BackendPebble {
		t.Fatalf("reopened backend = %q, want the recorded %q", got, BackendPebble)
	}
}
