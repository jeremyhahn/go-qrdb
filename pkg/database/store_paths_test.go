package database

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
)

// Every step of the WAL format marker write that fails names its path.
func TestWriteWALFormat_FailuresNameThePath(t *testing.T) {
	t.Run("directory not creatable", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(file, "wal")
		err := writeWALFormat(dir, filepath.Join(dir, walFormatMarker), WALFormatPlaintext)
		var marker *RaftWALMarkerError
		if !errors.As(err, &marker) || marker.Path != dir {
			t.Fatalf("err = %v, want *RaftWALMarkerError for %s", err, dir)
		}
	})
	t.Run("temporary file not creatable", func(t *testing.T) {
		dir := t.TempDir()
		m := filepath.Join(dir, walFormatMarker)
		if err := os.Mkdir(m+".tmp", 0o700); err != nil {
			t.Fatal(err)
		}
		err := writeWALFormat(dir, m, WALFormatPlaintext)
		var marker *RaftWALMarkerError
		if !errors.As(err, &marker) || marker.Path != m+".tmp" {
			t.Fatalf("err = %v, want *RaftWALMarkerError for the temporary file", err)
		}
	})
	t.Run("marker not replaceable", func(t *testing.T) {
		dir := t.TempDir()
		m := filepath.Join(dir, walFormatMarker)
		if err := os.MkdirAll(filepath.Join(m, "inner"), 0o700); err != nil {
			t.Fatal(err)
		}
		err := writeWALFormat(dir, m, WALFormatBarrier)
		var marker *RaftWALMarkerError
		if !errors.As(err, &marker) || marker.Path != m {
			t.Fatalf("err = %v, want *RaftWALMarkerError for the marker", err)
		}
	})
}

// ResolveDefaults never shares the caller's raft block, and a nil manager
// leaves the config otherwise as given.
func TestManager_ResolveDefaults(t *testing.T) {
	raft := &DatabaseRaftConfig{}
	cfg := DatabaseConfig{Name: "app", Backend: BackendRaft, Raft: raft}

	var nilMgr *Manager
	got := nilMgr.ResolveDefaults(cfg)
	if got.Raft == raft || !reflect.DeepEqual(got, cfg) {
		t.Fatalf("nil manager ResolveDefaults = %+v; want a copy of %+v with its own raft block", got, cfg)
	}

	d := Defaults{RaftClusterMode: RaftDistributed}
	m := newDescribingManager(t, &EmbeddedBackendFactory{})
	WithDefaults(d)(m)
	got = m.ResolveDefaults(cfg)
	want := cfg
	d.fill(&want)
	if got.Raft == raft || !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveDefaults = %+v, want %+v", got, want)
	}
	if raft.ClusterMode != "" {
		t.Fatalf("caller's raft block mutated: ClusterMode = %q", raft.ClusterMode)
	}
}

// A storage codec is checked against the codecs this build carries.
func TestDatabaseConfig_Validate_StorageCodecOfThisBuild(t *testing.T) {
	codecs := dao.DocumentCodecs()
	if len(codecs) == 0 {
		t.Fatal("this build carries no document codec")
	}
	ok := DatabaseConfig{Name: "app", Backend: BackendMemory, StorageCodec: string(codecs[len(codecs)-1])}
	if err := ok.Validate(); err != nil {
		t.Fatalf("Validate(%s) = %v", ok.StorageCodec, err)
	}
	bad := DatabaseConfig{Name: "app", Backend: BackendMemory, StorageCodec: "no-such-codec"}
	err := bad.Validate()
	var invalid *InvalidStorageCodecError
	if !errors.As(err, &invalid) || invalid.Codec != "no-such-codec" {
		t.Fatalf("Validate = %v, want *InvalidStorageCodecError", err)
	}
	if want := `database: invalid or unsupported storage codec: "no-such-codec"`; err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestRecordRootUnresolvedError_Error(t *testing.T) {
	cause := errors.New("tenant unknown")
	all := (&RecordRootUnresolvedError{Name: "a", Err: cause}).Error()
	if !strings.Contains(all, `database "a"`) || !strings.Contains(all, "catalog records' store roots") || !strings.Contains(all, cause.Error()) {
		t.Fatalf("Error() = %q", all)
	}
	one := (&RecordRootUnresolvedError{Name: "a", With: "b", TenantID: "acme", Err: cause}).Error()
	if !strings.Contains(one, `database "b" (tenant "acme")`) || !strings.Contains(one, cause.Error()) {
		t.Fatalf("Error() = %q", one)
	}
}

// IsSealed reports the layered barrier's state.
func TestBarrierBackend_IsSealed(t *testing.T) {
	barrier := &xorBarrier{mask: 0x5A}
	b := NewBarrierBackend(newTestBackend(t), barrier)
	if b.IsSealed() {
		t.Fatal("IsSealed with the barrier unsealed")
	}
	barrier.sealed = true
	if !b.IsSealed() {
		t.Fatal("IsSealed with the barrier sealed")
	}
}

// raftOptionSet names the first raft-only option a config sets.
func TestRaftOptionSet_NamesTheFirstRaftOption(t *testing.T) {
	cases := []struct {
		cfg  DatabaseConfig
		want string
	}{
		{DatabaseConfig{}, ""},
		{DatabaseConfig{Replicas: 3}, "replicas"},
		{DatabaseConfig{DedicatedWAL: true}, "dedicated_wal"},
		{DatabaseConfig{Raft: &DatabaseRaftConfig{}}, ""},
		{DatabaseConfig{Raft: &DatabaseRaftConfig{ClusterMode: RaftDistributed}}, "raft.clusterMode"},
		{DatabaseConfig{Raft: &DatabaseRaftConfig{ShardID: 7}}, "raft.shardId"},
		{DatabaseConfig{Raft: &DatabaseRaftConfig{ReplicaID: 2}}, "raft.replicaId"},
		{DatabaseConfig{Raft: &DatabaseRaftConfig{RaftAddress: "n1:7000"}}, "raft.raftAddress"},
		{DatabaseConfig{Raft: &DatabaseRaftConfig{Peers: []string{"n2:7000"}}}, "raft.peers"},
		{DatabaseConfig{Raft: &DatabaseRaftConfig{LocalNodeCount: 3}}, "raft.localNodeCount"},
		{DatabaseConfig{Raft: &DatabaseRaftConfig{StateStorage: BackendPebble}}, "raft.stateStorage"},
		{DatabaseConfig{Raft: &DatabaseRaftConfig{Discovery: DiscoveryStatic}}, "raft.discovery"},
	}
	for _, tc := range cases {
		if got := raftOptionSet(&tc.cfg); got != tc.want {
			t.Errorf("raftOptionSet(%+v) = %q, want %q", tc.cfg, got, tc.want)
		}
	}
}

// Installing a nil log reclaimer removes the installed one.
func TestManager_SetLogReclaimer_NilRemoves(t *testing.T) {
	m := newDescribingManager(t, &EmbeddedBackendFactory{})
	m.SetLogReclaimer(&recordingLogReclaimer{})
	if m.logReclaimer.Load() == nil {
		t.Fatal("SetLogReclaimer installed no reclaimer")
	}
	m.SetLogReclaimer(nil)
	if m.logReclaimer.Load() != nil {
		t.Fatal("a nil reclaimer left a reclaimer installed")
	}
}
