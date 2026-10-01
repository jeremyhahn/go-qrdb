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

package server

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// The etcd listener guards its streams with its database: the guard closes
// when the database is sealed, and the stream then ends with the sealed
// refusal (Unavailable); when the database is dropped, with its not-found.
func TestEtcdRequestAuthorizer_GuardStream(t *testing.T) {
	cases := []struct {
		name string
		stop func(*database.Database)
		want codes.Code
	}{
		{"sealed", func(db *database.Database) { db.SetStatus(database.StatusSealed) }, codes.Unavailable},
		{"dropped", func(db *database.Database) { _ = db.Close() }, codes.NotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newEtcdScopeFixture(t, true)
			a := &etcdRequestAuthorizer{mgr: f.mgr, owner: "acme", db: "app"}
			unserved, refusal, err := a.GuardStream(context.Background())
			if err != nil {
				t.Fatalf("GuardStream: %v", err)
			}
			select {
			case <-unserved:
				t.Fatal("the guard is closed while the database serves")
			default:
			}
			db, _ := f.mgr.Get("app")
			c.stop(db)
			select {
			case <-unserved:
			default:
				t.Fatal("the guard did not close when the database stopped serving")
			}
			if got := status.Code(refusal()); got != c.want {
				t.Fatalf("refusal = %v, want %v", refusal(), c.want)
			}
		})
	}
}

// A listener whose database is gone cannot guard a stream, and refuses it.
func TestEtcdRequestAuthorizer_GuardStreamUnknownDatabase(t *testing.T) {
	f := newEtcdScopeFixture(t, true)
	a := &etcdRequestAuthorizer{mgr: f.mgr, owner: "acme", db: "nosuchdb"}
	if _, _, err := a.GuardStream(context.Background()); status.Code(err) != codes.NotFound {
		t.Fatalf("GuardStream on a missing database = %v, want NotFound", err)
	}
}
