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
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// newSharedTransportTestHost builds a shared Host WITH a Raft transport — the
// clustered daemon's shape — so a dedicated shard started on it is subject to
// go-quicraft's member-identity rule (go-qrdb#556). No shard is started here.
func newSharedTransportTestHost(t *testing.T) (*quicraft.Host, uint64, string) {
	t.Helper()
	pki := t.TempDir()
	ca, err := qrdbdharness.NewCA(pki)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	leaf, err := ca.Issue(filepath.Join(pki, "node"), qrdbdharness.WithSubject("node-1", "raft"), qrdbdharness.WithServerAuth())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	caPEM, err := os.ReadFile(ca.CertPath)
	if err != nil {
		t.Fatalf("read CA: %v", err)
	}
	port, err := qrdbdharness.ReserveUDPPort()
	if err != nil {
		t.Fatalf("ReserveUDPPort: %v", err)
	}
	addr := "127.0.0.1:" + strconv.Itoa(port)
	dir := t.TempDir()
	cfg := quicraftconfig.HostConfig{
		WALDir:        filepath.Join(dir, "wal"),
		NodeHostDir:   filepath.Join(dir, "nodehost"),
		RaftAddress:   addr,
		ListenAddress: addr,
		DeploymentID:  1,
	}
	cfg.TransportConfig.MTLSConfig = &quicraftconfig.MTLSConfig{CACert: caPEM, Cert: leaf.CertPEM, Key: leaf.KeyPEM}
	host, err := quicraft.NewHost(cfg, quicraft.WithoutEncryption())
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	if host.NodeIdentity() == "" {
		t.Fatal("a host with a transport must report a node identity")
	}
	return host, sharedHostReplicaID, addr
}

// TestRaftBackendFactory_TransportHostFoundsDedicatedShard pins the dedicated
// shard's founding on a clustered shared host: the factory states the local
// replica's identity (the host's own), so go-quicraft admits the one founding
// member instead of refusing the start with InvalidMemberError.
func TestRaftBackendFactory_TransportHostFoundsDedicatedShard(t *testing.T) {
	host, replicaID, addr := newSharedTransportTestHost(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, replicaID, addr, reg, src, volTestLogger())
	t.Cleanup(func() { _ = f.CloseAll() })

	ctx := context.Background()
	dataRoot, _ := registerVolumeTenant(t, reg, "rt")
	const shardID = uint64(1000)
	src.add("rt", "app", shardID, dataRoot)

	svcs, err := f.Services(ctx, "rt", "app")
	if err != nil {
		t.Fatalf("Services on a transport host: %v", err)
	}
	if err := svcs.KV.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}
	if got, err := svcs.KV.Get(ctx, "k"); err != nil || string(got) != "v" {
		t.Fatalf("KV.Get = %q, %v; want v", got, err)
	}
}
