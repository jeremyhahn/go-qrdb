// Copyright 2026 Jeremy Hahn
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

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// transportConfigYAML sets every field of the transport: section to a value
// different from its default, so a field the daemon drops reads as its zero
// value and fails the comparison below.
const transportConfigYAML = `transport:
  max_message_bytes: 1048577
  max_conns_per_peer: 17
  max_streams_per_conn: 33
  client_rate_limit:
    rate_per_second: 2.5
    burst: 7
    trusted_proxies:
      - address: 192.0.2.0/24
        header: forwarded
      - address: 198.51.100.7
        header: x-forwarded-for
`

// TestBuildServerConfig_CarriesEveryTransportField pins that every field of
// the transport: section of a config file reaches the server config, not
// only client_rate_limit (go-qrdb#1197). The table names each field by its
// YAML key, and the test fails when TransportConfig gains a field the table
// does not name, so a new field cannot be dropped silently.
func TestBuildServerConfig_CarriesEveryTransportField(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "qrdbd.yaml")
	require.NoError(t, os.WriteFile(path, []byte(transportConfigYAML), 0o600))
	loaded, err := qrdbconfig.Load(path)
	require.NoError(t, err)

	f := &startFlags{mode: capabilities.ModeStandalone, adminPort: 63002, host: testBindHost}
	applyConfigToFlags(newStartCmd(), f, loaded)
	cfg, err := buildServerConfig(f)
	require.NoError(t, err)
	got := cfg.Transport

	cases := []struct {
		key  string
		want any
		got  any
	}{
		{key: "max_message_bytes", want: 1048577, got: got.MaxMessageBytes},
		{key: "max_conns_per_peer", want: 17, got: got.MaxConnsPerPeer},
		{key: "max_streams_per_conn", want: 33, got: got.MaxStreamsPerConn},
		{key: "client_rate_limit", want: qrdbconfig.ClientRateLimitConfig{
			RatePerSecond: 2.5,
			Burst:         7,
			TrustedProxies: []qrdbconfig.TrustedProxyConfig{
				{Address: "192.0.2.0/24", Header: "forwarded"},
				{Address: "198.51.100.7", Header: "x-forwarded-for"},
			},
		}, got: got.ClientRateLimit},
	}

	named := make(map[string]bool, len(cases))
	for _, tc := range cases {
		named[tc.key] = true
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.got)
		})
	}
	typ := reflect.TypeFor[qrdbconfig.TransportConfig]()
	for i := range typ.NumField() {
		key, _, _ := strings.Cut(typ.Field(i).Tag.Get("yaml"), ",")
		assert.Truef(t, named[key], "transport.%s has no case in this table", key)
	}
}

// TestBuildServerConfig_TransportAbsentStaysDefault pins that a config file
// without a transport: section leaves every field at its zero value, which
// resolves each bound to its default and applies no rate limit.
func TestBuildServerConfig_TransportAbsentStaysDefault(t *testing.T) {
	t.Parallel()

	f := &startFlags{mode: capabilities.ModeStandalone, adminPort: 63002, host: testBindHost}
	applyConfigToFlags(newStartCmd(), f, &qrdbconfig.Config{})
	cfg, err := buildServerConfig(f)
	require.NoError(t, err)

	assert.Equal(t, qrdbconfig.TransportConfig{}, cfg.Transport)
}
