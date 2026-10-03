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

//go:build qrdb_etcd

// The etcd leg of cluster_seal_transport_test.go. It lives in its own file
// because the etcd listener and its fixture compile only under qrdb_etcd.

package server

import (
	"context"
	"strings"
	"testing"
	"time"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// TestEtcdListener_RefusesWhileTheNodeRefuses pins the node-wide gate on the
// per-database etcd listener: while a cluster seal holds this node, cuts it
// off or is not yet confirmed, every etcd request and every stream open is
// refused with the typed cause -- as on every other transport -- and the
// listener serves again once the node does.
func TestEtcdListener_RefusesWhileTheNodeRefuses(t *testing.T) {
	cases := []struct {
		name  string
		local clusterseal.Local
		want  string
	}{
		{"held", clusterseal.LocalHeld, `barrier "node" sealed`},
		{"cut off", clusterseal.LocalCutOff, "cannot confirm the cluster seal state"},
		{"unconfirmed", clusterseal.LocalUnconfirmed, "cannot confirm the cluster seal state"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := newTestClusterSeal()
			f := newEtcdScopeFixture(t, true, WithClusterSeal(cs))
			etcdSrv, err := startEtcd(f.srv, "app", &database.EtcdConfig{Enabled: true, ListenAddr: "127.0.0.1:0"})
			if err != nil {
				t.Fatalf("startEtcd: %v", err)
			}
			t.Cleanup(func() { stopEtcd(etcdSrv) })
			conn, err := grpc.NewClient(etcdSrv.Addr(), grpc.WithTransportCredentials(credentials.NewTLS(f.clientTLS)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			kv := pb.NewKVClient(conn)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cs.SetLocal(tc.local)
			cs.setRefusing(true)
			_, err = kv.Range(ctx, &pb.RangeRequest{Key: []byte("visible")})
			if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Range while %s: err = %v, want Unavailable naming %q", tc.name, err, tc.want)
			}
			// The fixture's database serves no watch engine, so the stream
			// guard every etcd Watch and LeaseKeepAlive opens through is
			// driven directly, over this server's own refusal.
			guard := &etcdRequestAuthorizer{mgr: f.mgr, owner: "acme", db: "app",
				streams: cs.Streams(), nodeRefusal: f.srv.nodeRefusal}
			if _, _, err := guard.GuardStream(ctx); status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("stream open while %s: err = %v, want Unavailable naming %q", tc.name, err, tc.want)
			}
			if n := cs.Streams().Len(); n != 0 {
				t.Fatalf("a refused stream open left %d live subscriptions", n)
			}

			cs.setRefusing(false)
			cs.SetLocal(clusterseal.LocalServing)
			if _, err := kv.Range(ctx, &pb.RangeRequest{Key: []byte("visible")}); err != nil {
				t.Fatalf("Range once the node serves again: %v", err)
			}
		})
	}
}
