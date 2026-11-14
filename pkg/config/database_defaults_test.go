package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDatabaseDefaultsValidate(t *testing.T) {
	tests := []struct {
		name      string
		d         DatabaseDefaults
		wantField string
	}{
		{name: "empty", d: DatabaseDefaults{}},
		{name: "all set", d: DatabaseDefaults{Backend: "raft", Replicas: 3,
			Raft: DatabaseRaftDefaults{ClusterMode: "distributed", StateStorage: "file"}}},
		{name: "unknown backend", d: DatabaseDefaults{Backend: "rocks"}, wantField: "database_defaults.backend"},
		{name: "negative replicas", d: DatabaseDefaults{Replicas: -1}, wantField: "database_defaults.replicas"},
		{name: "unknown cluster mode", d: DatabaseDefaults{Raft: DatabaseRaftDefaults{ClusterMode: "global"}},
			wantField: "database_defaults.raft.clusterMode"},
		{name: "unknown state storage", d: DatabaseDefaults{Raft: DatabaseRaftDefaults{StateStorage: "raft"}},
			wantField: "database_defaults.raft.state_storage"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.d.Validate()
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			var de *DatabaseDefaultsError
			if !errors.As(err, &de) {
				t.Fatalf("Validate() = %v, want *DatabaseDefaultsError", err)
			}
			if de.Field != tc.wantField {
				t.Fatalf("Field = %q, want %q", de.Field, tc.wantField)
			}
		})
	}
}

func TestConfigValidateRefusesBadDatabaseDefaults(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DatabaseDefaults.Backend = "rocks"
	var de *DatabaseDefaultsError
	if err := cfg.Validate(); !errors.As(err, &de) {
		t.Fatalf("Validate() = %v, want *DatabaseDefaultsError", err)
	}
}

func TestLoadDatabaseDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qrdb.yaml")
	body := "database_defaults:\n  backend: raft\n  replicas: 3\n  raft:\n    clusterMode: distributed\n    state_storage: file\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	want := DatabaseDefaults{Backend: "raft", Replicas: 3,
		Raft: DatabaseRaftDefaults{ClusterMode: "distributed", StateStorage: "file"}}
	if cfg.DatabaseDefaults != want {
		t.Fatalf("DatabaseDefaults = %+v, want %+v", cfg.DatabaseDefaults, want)
	}
}
