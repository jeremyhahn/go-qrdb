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

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTUpdateDatabase_SendsTheDocumentedRequest drives each
// UpdateDatabase* method against a server that records what it receives: the
// method must PATCH its own sub-resource with exactly the documented body
// (the whole request for some, only the inner config for others) and decode
// the DatabaseInfo the server answers.
func TestRESTUpdateDatabase_SendsTheDocumentedRequest(t *testing.T) {
	sql := transport.UpdateDatabaseSQLRequest{Name: "shop", SQL: &transport.DatabaseSQLConfig{Concurrency: "pessimistic"}}
	protocol := transport.UpdateDatabaseProtocolRequest{Name: "shop", MySQL: true}
	etcd := transport.UpdateDatabaseEtcdRequest{Name: "shop", Enabled: true, ListenAddr: "127.0.0.1:2379"}
	raft := transport.UpdateDatabaseRaftRequest{Name: "shop", Raft: &transport.DatabaseRaftConfig{}}
	connection := transport.UpdateDatabaseConnectionRequest{Name: "shop", Connection: &transport.DatabaseConnectionConfig{}}

	cases := []struct {
		name     string
		path     string
		wantBody any
		call     func(ctx context.Context, rt *Transport) (*transport.DatabaseInfo, error)
	}{
		{"UpdateDatabaseSQL", "/v1/databases/shop/sql", sql, func(ctx context.Context, rt *Transport) (*transport.DatabaseInfo, error) {
			return rt.UpdateDatabaseSQL(ctx, sql)
		}},
		{"UpdateDatabaseProtocol", "/v1/databases/shop/protocol", protocol, func(ctx context.Context, rt *Transport) (*transport.DatabaseInfo, error) {
			return rt.UpdateDatabaseProtocol(ctx, protocol)
		}},
		{"UpdateDatabaseEtcd", "/v1/databases/shop/etcd", etcd, func(ctx context.Context, rt *Transport) (*transport.DatabaseInfo, error) {
			return rt.UpdateDatabaseEtcd(ctx, etcd)
		}},
		{"UpdateDatabaseRaft", "/v1/databases/shop/raft", raft.Raft, func(ctx context.Context, rt *Transport) (*transport.DatabaseInfo, error) {
			return rt.UpdateDatabaseRaft(ctx, raft)
		}},
		{"UpdateDatabaseConnection", "/v1/databases/shop/connection", connection.Connection, func(ctx context.Context, rt *Transport) (*transport.DatabaseInfo, error) {
			return rt.UpdateDatabaseConnection(ctx, connection)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want, err := json.Marshal(c.wantBody)
			if err != nil {
				t.Fatalf("marshal want: %v", err)
			}
			rt, rs, _ := newExtTransport(t)
			var gotBody []byte
			rs.handle(func(r *http.Request) bool {
				return r.Method == http.MethodPatch && r.URL.Path == c.path
			}, func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				writeJSON(w, &transport.DatabaseInfo{Name: "shop"})
			})

			info, err := c.call(context.Background(), rt)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if !bytes.Equal(bytes.TrimSpace(gotBody), want) {
				t.Fatalf("PATCH %s body = %s, want %s", c.path, gotBody, want)
			}
			if info.Name != "shop" {
				t.Fatalf("DatabaseInfo.Name = %q, want shop", info.Name)
			}
		})
	}
}

func TestRESTUpdateDatabase_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(context.Context, *Transport) error{
		"UpdateDatabaseSQL": func(ctx context.Context, rt *Transport) error {
			_, err := rt.UpdateDatabaseSQL(ctx, transport.UpdateDatabaseSQLRequest{Name: "shop"})
			return err
		},
		"UpdateDatabaseProtocol": func(ctx context.Context, rt *Transport) error {
			_, err := rt.UpdateDatabaseProtocol(ctx, transport.UpdateDatabaseProtocolRequest{Name: "shop"})
			return err
		},
		"UpdateDatabaseEtcd": func(ctx context.Context, rt *Transport) error {
			_, err := rt.UpdateDatabaseEtcd(ctx, transport.UpdateDatabaseEtcdRequest{Name: "shop"})
			return err
		},
		"UpdateDatabaseRaft": func(ctx context.Context, rt *Transport) error {
			_, err := rt.UpdateDatabaseRaft(ctx, transport.UpdateDatabaseRaftRequest{Name: "shop"})
			return err
		},
		"UpdateDatabaseBarrier": func(ctx context.Context, rt *Transport) error {
			_, err := rt.UpdateDatabaseBarrier(ctx, transport.UpdateDatabaseBarrierRequest{Name: "shop"})
			return err
		},
		"UpdateDatabaseConnection": func(ctx context.Context, rt *Transport) error {
			_, err := rt.UpdateDatabaseConnection(ctx, transport.UpdateDatabaseConnectionRequest{Name: "shop"})
			return err
		},
	})
}
