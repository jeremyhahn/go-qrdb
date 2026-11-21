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

package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
	"github.com/jeremyhahn/go-qrdb/pkg/store/storetest"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestNewDataPlaneCreateFunc_EveryStateMachineHasItsOwnLogs runs the node
// create function's two-groups proof over the data-plane create function a
// tenant's and a database's groups are built from: each state machine gets
// its own log registry, so no group's audit chain or named log shares heads
// with another's.
func TestNewDataPlaneCreateFunc_EveryStateMachineHasItsOwnLogs(t *testing.T) {
	_, tb := registerVolumeTenant(t, newVolumePooledRegistry(t), "acme")
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	storetest.RequireOwnLogsPerStateMachine(t, newDataPlaneCreateFunc(tb, lease.NewLessor(), hub))
}

// TestNewDataPlaneCreateFunc_LogKeysSurviveSnapshotRestore pins that a barrier
// tenant's log and its sealed head-signing key travel with the group: a
// snapshot restored into a fresh state machine serves the same key record and
// head, the private key is never in plaintext in either state machine's
// directory, and once the tenant's barrier is sealed the snapshot cannot be
// restored at all. The snapshot stream itself is the group's logical state;
// the Raft host writes it to disk under the tenant's barrier, as it does the
// WAL.
func TestNewDataPlaneCreateFunc_LogKeysSurviveSnapshotRestore(t *testing.T) {
	ctx := context.Background()
	_, tb := registerVolumeTenant(t, newVolumePooledRegistry(t), "acme")
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	createFn := newDataPlaneCreateFunc(tb, lease.NewLessor(), hub)

	open := func(t *testing.T, shardID uint64, dir string) *store.MVCCStateMachine {
		t.Helper()
		m, ok := createFn(shardID, 1).(*store.MVCCStateMachine)
		if !ok {
			t.Fatalf("the data-plane create function built a %T, want *store.MVCCStateMachine", createFn(shardID, 1))
		}
		if _, err := m.Open(ctx, dir, make(chan struct{})); err != nil {
			t.Fatalf("open %d: %v", shardID, err)
		}
		t.Cleanup(func() { _ = m.Close(ctx) })
		return m
	}
	apply := func(t *testing.T, m *store.MVCCStateMachine) func([]byte, error) {
		return func(cmd []byte, err error) {
			t.Helper()
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			result, err := backend.NewLocalBackend(m).SyncPropose(ctx, 1, cmd)
			if err != nil {
				t.Fatalf("propose: %v", err)
			}
			if msg, ok := protocol.DecodeAppError(result.Data); ok {
				t.Fatalf("propose refused: %s", msg)
			}
		}
	}

	id := vlog.LogID{TenantID: "acme", Database: "ledger", Log: "events"}
	key, err := vlog.GenerateSigningKey(vlog.SignatureEd25519, vlog.FirstKeyVersion)
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	signer, _, err := key.Signer()
	if err != nil {
		t.Fatalf("Signer: %v", err)
	}
	hk, err := vlog.NewHeadKey(key.Version, vlog.KeyRef(key.Version), signer.Public())
	if err != nil {
		t.Fatalf("NewHeadKey: %v", err)
	}
	desc := vlog.Descriptor{Origin: "acme.example.com/events", Hash: vlog.HashSHA256, Keys: []vlog.HeadKey{hk}}

	dirA, dirB := t.TempDir(), t.TempDir()
	leader := open(t, 1, dirA)
	apply(t, leader)(protocol.EncodeVerifiableLogCreateWithKey(id, desc, key))
	apply(t, leader)(protocol.EncodeVerifiableLogAppend(id, [][]byte{[]byte("entry")}))
	head, err := vlog.ReadHead(leader.Store().Storage(), id)
	if err != nil {
		t.Fatalf("leader ReadHead: %v", err)
	}
	apply(t, leader)(protocol.EncodeVerifiableLogCheckpoint(id, head.Size, head.Root, []byte("signed checkpoint")))
	head, err = vlog.ReadHead(leader.Store().Storage(), id)
	if err != nil {
		t.Fatalf("leader ReadHead after the checkpoint: %v", err)
	}

	var buf bytes.Buffer
	snap, err := leader.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	if err := leader.SaveSnapshot(ctx, snap, &buf, make(chan struct{})); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	snapshot := buf.Bytes()

	follower := open(t, 2, dirB)
	if err := follower.RecoverFromSnapshot(ctx, bytes.NewReader(snapshot), make(chan struct{})); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}
	got, err := vlog.ReadSigningKey(follower.Store().Storage(), id, key.Version)
	if err != nil || !reflect.DeepEqual(*got, key) {
		t.Fatalf("follower ReadSigningKey = %+v, %v; want the leader's key", got, err)
	}
	gotHead, err := vlog.ReadHead(follower.Store().Storage(), id)
	if err != nil || !reflect.DeepEqual(gotHead, head) {
		t.Fatalf("follower ReadHead = %+v, %v; want the leader's %+v", gotHead, err, head)
	}
	d, err := vlog.ReadDescriptor(follower.Store().Storage(), id)
	if err != nil || !reflect.DeepEqual(d.Keys, desc.Keys) {
		t.Fatalf("follower ReadDescriptor = %+v, %v; want keys %+v", d, err, desc.Keys)
	}

	// Neither the PKCS#8 encoding nor the raw Ed25519 seed is in any file.
	secrets := [][]byte{key.PKCS8, key.PKCS8[len(key.PKCS8)-ed25519.SeedSize:]}
	for _, dir := range []string{dirA, dirB} {
		err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
			if err != nil || !e.Type().IsRegular() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, secret := range secrets {
				if bytes.Contains(data, secret) {
					t.Errorf("%s holds the private key in plaintext", path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	third := open(t, 3, t.TempDir())
	if err := tb.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Pebble's sealed refusal cannot carry pkg/seal's sentinel (pkg/seal
	// imports the Pebble storage), so either names the sealed barrier.
	err = third.RecoverFromSnapshot(ctx, bytes.NewReader(snapshot), make(chan struct{}))
	if !errors.Is(err, seal.ErrSealed) && !errors.Is(err, pebblestore.ErrBarrierSealed) {
		t.Fatalf("RecoverFromSnapshot with the barrier sealed = %v, want seal.ErrSealed or pebble.ErrBarrierSealed", err)
	}
	if _, err := vlog.ReadHead(third.Store().Storage(), id); err == nil {
		t.Fatal("the sealed recover left the log readable in the third state machine")
	}
}
