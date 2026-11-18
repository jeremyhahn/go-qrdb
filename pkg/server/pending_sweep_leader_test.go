// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// fakeHomes places tenants by name; an entry in fail makes InHome error.
type fakeHomes struct {
	home map[string]bool
	fail map[string]bool
}

func (f fakeHomes) InHome(_ context.Context, tenantID string) (bool, error) {
	if f.fail[tenantID] {
		return false, errors.New("placement unavailable")
	}
	return f.home[tenantID], nil
}

func TestPendingSweepLeader_RoutesEachTenantToItsGroupLeader(t *testing.T) {
	ctx := context.Background()
	homes := fakeHomes{
		home: map[string]bool{"iso": true},
		fail: map[string]bool{"broken": true},
	}
	type nodeView struct {
		leadsSystem bool
		leadsHome   map[string]bool
	}
	cases := []struct {
		name   string
		node   nodeView
		tenant string
		want   bool
	}{
		// The system leader holds "iso" sealed: it must not sweep it.
		{"system leader skips an isolated tenant it does not home-lead", nodeView{true, nil}, "iso", false},
		// The home leader is not the system leader: it still sweeps "iso".
		{"home leader sweeps its isolated tenant", nodeView{false, map[string]bool{"iso": true}}, "iso", true},
		{"system leader sweeps the system tenant", nodeView{true, nil}, tenantid.System, true},
		{"system leader sweeps a shared tenant", nodeView{true, nil}, "shared", true},
		{"a follower sweeps no shared tenant", nodeView{false, map[string]bool{"iso": true}}, "shared", false},
		{"a failed placement sweeps nothing", nodeView{true, map[string]bool{"broken": true}}, "broken", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node := tc.node
			gate := PendingSweepLeader(
				func() bool { return node.leadsSystem },
				homes,
				func(id string) bool { return node.leadsHome[id] },
			)
			if got := gate(ctx, tc.tenant); got != tc.want {
				t.Fatalf("gate(%q) = %v, want %v", tc.tenant, got, tc.want)
			}
		})
	}
}

func TestPendingSweepLeader_NilParts(t *testing.T) {
	ctx := context.Background()
	if !PendingSweepLeader(nil, nil, nil)(ctx, "any") {
		t.Fatal("standalone (all nil) must sweep every tenant")
	}
	homes := fakeHomes{home: map[string]bool{"iso": true}}
	if PendingSweepLeader(nil, homes, nil)(ctx, "iso") {
		t.Fatal("no home-leader check must never sweep an isolated tenant")
	}
}
