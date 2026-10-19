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
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// refusingRegistry is a registry that refuses stream opens for one database.
type refusingRegistry struct {
	service.Registry
	tenant, db string
	refusal    error
}

var _ service.StreamRefuser = (*refusingRegistry)(nil)

func (r *refusingRegistry) StreamRefusal(tenantID, db string) error {
	if tenantID == r.tenant && db == r.db {
		return r.refusal
	}
	return nil
}

// TestEtcdStreamRefusal_BindsTheDatabase proves the stream-refusal check is
// bound to the listener's owner and database when the registry can refuse,
// and absent when it cannot.
func TestEtcdStreamRefusal_BindsTheDatabase(t *testing.T) {
	boom := errors.New("database is being dropped")
	reg := &refusingRegistry{tenant: "acme", db: "app", refusal: boom}

	if check := etcdStreamRefusal(reg, "acme", "app"); check == nil || !errors.Is(check(), boom) {
		t.Fatal("the bound check did not report the registry's refusal for acme/app")
	}
	if check := etcdStreamRefusal(reg, "acme", "other"); check == nil || check() != nil {
		t.Fatal("the bound check refused a database the registry serves")
	}
	if check := etcdStreamRefusal(struct{ service.Registry }{}, "acme", "app"); check != nil {
		t.Fatal("a registry that cannot refuse streams produced a refusal check")
	}
}

// TestEtcdRequestAuthorizer_OpenRefusal proves a stream open is refused, as a
// gRPC status naming the cause, by the node gate first and then by the
// database's own stream refusal, and admitted when neither refuses.
func TestEtcdRequestAuthorizer_OpenRefusal(t *testing.T) {
	nodeErr := errors.New("node sealed")
	dbErr := errors.New("database is being dropped")
	cases := map[string]struct {
		node, stream func() error
		want         string
	}{
		"admitted":        {nil, nil, ""},
		"both pass":       {func() error { return nil }, func() error { return nil }, ""},
		"node refuses":    {func() error { return nodeErr }, func() error { return dbErr }, nodeErr.Error()},
		"stream refuses":  {nil, func() error { return dbErr }, dbErr.Error()},
		"node passes too": {func() error { return nil }, func() error { return dbErr }, dbErr.Error()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := &etcdRequestAuthorizer{nodeRefusal: tc.node, streamRefusal: tc.stream}
			err := a.openRefusal()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("openRefusal = %v, want nil", err)
				}
				return
			}
			if status.Code(err) == codes.OK || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("openRefusal = %v, want a gRPC status naming %q", err, tc.want)
			}
		})
	}
}
