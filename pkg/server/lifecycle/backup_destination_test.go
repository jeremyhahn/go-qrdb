package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/backupsink"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

func TestOpenBackupDestination_LocalRoundTrips(t *testing.T) {
	dir := t.TempDir()
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
