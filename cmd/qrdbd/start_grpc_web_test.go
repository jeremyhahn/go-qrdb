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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// TestApplyConfigToFlags_GRPCWebFromConfig pins that the config file's
// server.grpc_web section reaches startFlags: no flag moves it, so the config
// file is its only source, and dropping it left gRPC-Web unserved on a node
// whose configuration enabled it.
func TestApplyConfigToFlags_GRPCWebFromConfig(t *testing.T) {
	t.Parallel()

	want := qrdbconfig.GRPCWebConfig{
		Enabled:          true,
		AllowedOrigins:   []string{"https://app.example.com"},
		AllowCredentials: true,
	}
	cmd := newStartCmd()
	f := &startFlags{mode: "raft", host: testBindHost}
	cfg := &qrdbconfig.Config{Server: qrdbconfig.ServerConfig{GRPCWeb: want}}
	applyConfigToFlags(cmd, f, cfg)

	assert.Equal(t, want, f.grpcWeb)
}

// TestApplyConfigToFlags_GRPCWebAbsentStaysOff pins that a config file with
// no server.grpc_web section leaves gRPC-Web off.
func TestApplyConfigToFlags_GRPCWebAbsentStaysOff(t *testing.T) {
	t.Parallel()

	cmd := newStartCmd()
	f := &startFlags{mode: "raft", host: testBindHost}
	applyConfigToFlags(cmd, f, &qrdbconfig.Config{})

	assert.Equal(t, qrdbconfig.GRPCWebConfig{}, f.grpcWeb)
}

// TestBuildServerConfig_CarriesGRPCWeb pins that buildServerConfig hands the
// server the gRPC-Web configuration startFlags holds, enabled or not.
func TestBuildServerConfig_CarriesGRPCWeb(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		web  qrdbconfig.GRPCWebConfig
	}{
		{name: "enabled", web: qrdbconfig.GRPCWebConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.com"}}},
		{name: "default off", web: qrdbconfig.GRPCWebConfig{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := &startFlags{mode: capabilities.ModeStandalone, adminPort: 63002, host: testBindHost, grpcWeb: tc.web}
			cfg, err := buildServerConfig(f)
			require.NoError(t, err)
			assert.Equal(t, tc.web, cfg.Server.GRPCWeb)
		})
	}
}
