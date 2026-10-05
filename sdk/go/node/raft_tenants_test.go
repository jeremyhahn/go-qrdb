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

package node

import (
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// A node is BYO-TLS only when it is handed its cluster identity and no
// cluster CA key of its own.
func TestByoTLS(t *testing.T) {
	id := &RaftMTLSConfig{CACert: []byte("ca")}
	for name, tc := range map[string]struct {
		n    *Node
		want bool
	}{
		"a cluster identity and no CA key": {n: &Node{clusterIdentity: id}, want: true},
		"no cluster identity":              {n: &Node{}},
		"a CA key provider":                {n: &Node{clusterIdentity: id, clusterCAKeyProvider: nopCAProvider{}}},
		"CA material to adopt":             {n: &Node{clusterIdentity: id, clusterCACertPEM: []byte("cert")}},
	} {
		if got := tc.n.byoTLS(); got != tc.want {
			t.Errorf("%s: byoTLS = %v, want %v", name, got, tc.want)
		}
	}
}

// nopCAProvider is a cluster CA key provider the test never calls.
type nopCAProvider struct{ cakey.Provider }

// A node composes the raft tenant cluster only when it is clustered: a
// standalone node, one with no Raft host, or no configuration composes
// nothing.
func TestRaftTenantClusterOptions_NothingOffACluster(t *testing.T) {
	n := &Node{logger: slog.New(slog.DiscardHandler)}
	cfg := qrdbconfig.DefaultConfig()
	for name, tc := range map[string]struct {
		cfg        *qrdbconfig.Config
		standalone bool
	}{
		"standalone":       {cfg: cfg, standalone: true},
		"no Raft host":     {cfg: cfg},
		"no configuration": {},
	} {
		if opts := n.raftTenantClusterOptions(&lifecycle.Node{}, tc.cfg, nil, tc.standalone); opts != nil {
			t.Errorf("%s: composed %d server options; want none", name, len(opts))
		}
	}
}
