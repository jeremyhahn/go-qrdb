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

// TestApplyConfigToFlags_CORSFromConfig pins that the config file's cors
// section reaches startFlags: no flag moves it, so the config file is its
// only source, and dropping it left the REST server sending no CORS headers
// whatever the section said.
func TestApplyConfigToFlags_CORSFromConfig(t *testing.T) {
	t.Parallel()

	want := qrdbconfig.CORSConfig{
		AllowedOrigins: []string{"https://app.example.com"},
		AllowedHeaders: []string{"Authorization"},
		MaxAge:         600,
	}
	cmd := newStartCmd()
	f := &startFlags{mode: "raft", host: testBindHost}
	applyConfigToFlags(cmd, f, &qrdbconfig.Config{CORS: want})

	assert.Equal(t, want, f.cors)
}

// TestApplyConfigToFlags_CORSAbsentStaysOff pins that a config file with no
// cors section leaves CORS off.
func TestApplyConfigToFlags_CORSAbsentStaysOff(t *testing.T) {
	t.Parallel()

	cmd := newStartCmd()
	f := &startFlags{mode: "raft", host: testBindHost}
	applyConfigToFlags(cmd, f, &qrdbconfig.Config{})

	assert.Empty(t, f.cors.AllowedOrigins)
}

// TestBuildServerConfig_CarriesCORS pins that buildServerConfig hands the
// server the cors section startFlags holds, set or not.
func TestBuildServerConfig_CarriesCORS(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		cors qrdbconfig.CORSConfig
	}{
		{name: "origin listed", cors: qrdbconfig.CORSConfig{AllowedOrigins: []string{"https://app.example.com"}, MaxAge: 600}},
		{name: "default off", cors: qrdbconfig.CORSConfig{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := &startFlags{mode: capabilities.ModeStandalone, adminPort: 63002, host: testBindHost, cors: tc.cors}
			cfg, err := buildServerConfig(f)
			require.NoError(t, err)
			assert.Equal(t, tc.cors, cfg.CORS)
		})
	}
}
