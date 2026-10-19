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

package main

import (
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/internal/netport"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// TestApplyConfigToFlags_JoinAndAdvertiseFields covers the config fields
// that only reach the flags when the file sets them: the SM storage, the
// member identities, the advertise host, the bootstrap join settings and the
// cluster-join listener.
func TestApplyConfigToFlags_JoinAndAdvertiseFields(t *testing.T) {
	cfg := &qrdbconfig.Config{}
	cfg.Raft.SMStorage = "file"
	cfg.Raft.MemberIdentities = "spiffe://a,spiffe://b"
	cfg.Server.AdvertiseHost = "db.example.com"
	cfg.Bootstrap.JoinAddr = "db.example.com:7000"
	cfg.Bootstrap.JoinKey = "join-key"
	cfg.Bootstrap.JoinHTTPURL = "https://db.example.com/join"
	cfg.Bootstrap.JoinTokenFile = "/run/secrets/join-token"
	cfg.Bootstrap.JoinApprovalPollInterval = 3 * time.Second
	cfg.Bootstrap.JoinTokenEnv = "QRDB_JOIN_TOKEN"
	cfg.ClusterJoin.ListenHost = "10.0.0.5"
	cfg.ClusterJoin.ListenPort = 7443

	f := &startFlags{}
	applyConfigToFlags(newStartCmd(), f, cfg)

	checks := []struct {
		name      string
		got, want any
	}{
		{"smStorage", f.smStorage, "file"},
		{"advertiseHost", f.advertiseHost, "db.example.com"},
		{"bootstrapJoinAddr", f.bootstrapJoinAddr, "db.example.com:7000"},
		{"bootstrapJoinKey", f.bootstrapJoinKey, "join-key"},
		{"bootstrapJoinHTTPURL", f.bootstrapJoinHTTPURL, "https://db.example.com/join"},
		{"bootstrapJoinTokenFile", f.bootstrapJoinTokenFile, "/run/secrets/join-token"},
		{"bootstrapJoinApprovalPollInterval", f.bootstrapJoinApprovalPollInterval, 3 * time.Second},
		{"bootstrapJoinTokenEnv", f.bootstrapJoinTokenEnv, "QRDB_JOIN_TOKEN"},
		{"clusterJoinListenHost", f.clusterJoinListenHost, "10.0.0.5"},
		{"clusterJoinListenPort", f.clusterJoinListenPort, 7443},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if !slices.Equal(f.memberIdentities, []string{"spiffe://a", "spiffe://b"}) {
		t.Errorf("memberIdentities = %v, want [spiffe://a spiffe://b]", f.memberIdentities)
	}
}

// TestApplyConfigToFlags_ExplicitFlagsWin proves a flag the operator set
// is not overwritten by the same setting in the config file.
func TestApplyConfigToFlags_ExplicitFlagsWin(t *testing.T) {
	cmd := newStartCmd()
	for name, value := range map[string]string{
		"sm-storage":               "memory",
		"advertise-host":           "flag.example.com",
		"bootstrap-join-addr":      "flag.example.com:7000",
		"cluster-join-listen-port": "9443",
	} {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatalf("Set(%q): %v", name, err)
		}
	}
	cfg := &qrdbconfig.Config{}
	cfg.Raft.SMStorage = "file"
	cfg.Server.AdvertiseHost = "db.example.com"
	cfg.Bootstrap.JoinAddr = "db.example.com:7000"
	cfg.ClusterJoin.ListenPort = 7443

	f := &startFlags{smStorage: "memory", advertiseHost: "flag.example.com", bootstrapJoinAddr: "flag.example.com:7000", clusterJoinListenPort: 9443}
	applyConfigToFlags(cmd, f, cfg)

	if f.smStorage != "memory" || f.advertiseHost != "flag.example.com" ||
		f.bootstrapJoinAddr != "flag.example.com:7000" || f.clusterJoinListenPort != 9443 {
		t.Fatalf("config overwrote explicit flags: %+v", f)
	}
}

// TestResolveAutoConfig_PortProbeFailure covers each auto-selected listener
// port whose probe fails. The host is from TEST-NET-1 (RFC 5737), which no
// interface holds, so every bind is refused locally without any traffic.
// Each case marks the earlier ports as explicit so the probe under test is
// the first one to run.
func TestResolveAutoConfig_PortProbeFailure(t *testing.T) {
	const unboundHost = "192.0.2.1"
	tests := []struct {
		name     string
		explicit []string
		quic     bool
	}{
		{name: "admin port", explicit: []string{"listen"}},
		{name: "grpc port", explicit: []string{"listen", "admin-port"}},
		{name: "quic port", explicit: []string{"listen", "admin-port", "grpc-port"}, quic: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newStartCmd()
			for _, name := range tt.explicit {
				value := "1"
				if name == "listen" {
					value = "0.0.0.0:1"
				}
				if err := cmd.Flags().Set(name, value); err != nil {
					t.Fatalf("Set(%q): %v", name, err)
				}
			}
			f := &startFlags{
				discoveryMethod: "multicast",
				host:            unboundHost,
				enableQUIC:      tt.quic,
				dataDir:         t.TempDir(),
			}
			err := resolveAutoConfig(cmd, f)
			var portErr *netport.NoFreePortError
			if !errors.As(err, &portErr) {
				t.Fatalf("resolveAutoConfig() error = %v (%T), want *netport.NoFreePortError", err, err)
			}
			if portErr.Host != unboundHost {
				t.Fatalf("NoFreePortError.Host = %q, want %q", portErr.Host, unboundHost)
			}
		})
	}
}

// TestRun_ExitCodes drives the daemon's root command through run() for a
// command that succeeds and for one cobra rejects.
func TestRun_ExitCodes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "version", args: []string{"qrdbd", "version"}, want: 0},
		{name: "unknown command", args: []string{"qrdbd", "no-such-command"}, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			saved := os.Args
			t.Cleanup(func() { os.Args = saved })
			os.Args = tt.args
			if got := run(); got != tt.want {
				t.Fatalf("run() = %d, want %d", got, tt.want)
			}
		})
	}
}
