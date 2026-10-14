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

// Tests for the bind-host configuration path (go-qrdb#167).
//
// The defect these cover is not "a literal appears in the source" but "a value
// the operator set never arrives": server.host was defined, validated and
// viper-registered, and then buildServerConfig replaced it with a literal on
// the one path that reaches server.New. Every assertion here therefore follows
// a value from a configuration source through to the *qrdbconfig.Config the
// orchestrator hands each listener.
package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// testBindHost is the bind host tests use when they build a startFlags by
// hand and then start something that listens. A startFlags built directly is
// not a resolved configuration -- in production cobra's --host default fills
// this in before anything reads it -- so a hand-built one has to supply it,
// and loopback keeps the suite from opening wildcard sockets on the build
// host.
const testBindHost = "127.0.0.1"

// writeBindHostConfig writes a minimal valid config file containing the given
// server section body and returns its path.
func writeBindHostConfig(t *testing.T, serverBody string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "qrdb.yaml")
	body := `mode: "raft"
server:
` + serverBody + `
protocols:
  rest: true
raft:
  data_dir: "` + filepath.Join(dir, "data") + `"
  deployment_id: 1
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:19001"
  peers: "1=127.0.0.1:19001"
tls:
  enabled: false
logging:
  level: "info"
  format: "text"
discovery:
  method: "static"
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// resolveWithArgs parses args and resolves the configuration exactly as the
// start command does, returning the resolved flags.
func resolveWithArgs(t *testing.T, args ...string) *startFlags {
	t.Helper()
	cmd, f := newStartCmdWithFlags()
	require.NoError(t, cmd.ParseFlags(args))
	require.NoError(t, resolveStartFlags(cmd, f))
	return f
}

// serverHostOf builds the server config the daemon hands server.New and
// returns the bind host it carries, failing the test if the build is refused.
func serverHostOf(t *testing.T, f *startFlags) string {
	t.Helper()
	cfg, err := buildServerConfig(f)
	require.NoError(t, err)
	return cfg.Server.Host
}

// TestBuildServerConfig_CarriesResolvedHost is the assertion the whole issue
// turns on: whatever host resolution produced has to be the host in the
// *qrdbconfig.Config handed to server.New, because that struct field is what
// every listener joins with its port.
func TestBuildServerConfig_CarriesResolvedHost(t *testing.T) {
	f := &startFlags{host: "10.1.2.3", adminPort: 63002, grpcPort: 63003}
	assert.Equal(t, "10.1.2.3", serverHostOf(t, f))
}

// TestBindHost_DefaultsToDefaultsLayer proves the flag default is the
// defaults-layer value and not a second, independent literal.
func TestBindHost_DefaultsToDefaultsLayer(t *testing.T) {
	f := resolveWithArgs(t)
	assert.Equal(t, qrdbconfig.DefaultServerHost, f.host)
	assert.Equal(t, qrdbconfig.DefaultConfig().Server.Host, f.host)
	assert.Equal(t, qrdbconfig.DefaultServerHost, serverHostOf(t, f))
}

// TestBindHost_FromConfigFile: server.host in YAML reaches the server config.
// This is the path that was silently discarded.
func TestBindHost_FromConfigFile(t *testing.T) {
	cfgPath := writeBindHostConfig(t, `  host: "127.0.0.1"
  admin_port: 19443`)
	f := resolveWithArgs(t, "--config", cfgPath)
	assert.Equal(t, "127.0.0.1", f.host)
	assert.Equal(t, "127.0.0.1", serverHostOf(t, f))
}

// TestBindHost_FromEnv: QRDB_SERVER_HOST reaches the server config and beats
// the file.
func TestBindHost_FromEnv(t *testing.T) {
	cfgPath := writeBindHostConfig(t, `  host: "127.0.0.1"
  admin_port: 19443`)
	t.Setenv("QRDB_SERVER_HOST", "10.9.9.9")
	f := resolveWithArgs(t, "--config", cfgPath)
	assert.Equal(t, "10.9.9.9", f.host)
	assert.Equal(t, "10.9.9.9", serverHostOf(t, f))
}

// TestBindHost_FlagBeatsEnvAndFile pins the documented precedence
// flags > env > file > defaults on this key.
func TestBindHost_FlagBeatsEnvAndFile(t *testing.T) {
	cfgPath := writeBindHostConfig(t, `  host: "127.0.0.1"
  admin_port: 19443`)
	t.Setenv("QRDB_SERVER_HOST", "10.9.9.9")
	f := resolveWithArgs(t, "--config", cfgPath, "--host", "192.168.5.5")
	assert.Equal(t, "192.168.5.5", f.host)
	assert.Equal(t, "192.168.5.5", serverHostOf(t, f))
}

// TestBuildServerConfig_FailsClosedWithoutHost: an absent bind host is refused,
// not treated as "every interface".
//
// This is the guard the pre-existing TestBuildServerConfig failure pointed at.
// net.JoinHostPort("", port) is ":port" and net.Listen binds that to [::], so
// an empty host would have silently produced the widest possible bind -- by a
// stdlib convention rather than by anything an operator could read out of the
// configuration. Unreachable through the command (cobra applies
// qrdbconfig.DefaultServerHost at flag registration, before any parsing); this
// guards direct constructions.
func TestBuildServerConfig_FailsClosedWithoutHost(t *testing.T) {
	cfg, err := buildServerConfig(&startFlags{adminPort: 63002})
	assert.Nil(t, cfg)
	var missing *MissingBindHostError
	require.True(t, errors.As(err, &missing), "error = %v (%T), want *MissingBindHostError", err, err)
}

// TestNoiseHost_DefaultsToServerHost: the enrollment listener follows the API
// bind host unless it is given one of its own, so containing a node to an
// interface contains enrollment too.
func TestNoiseHost_DefaultsToServerHost(t *testing.T) {
	cfgPath := writeBindHostConfig(t, `  host: "127.0.0.1"
  admin_port: 19443`)
	f := resolveWithArgs(t, "--config", cfgPath)
	assert.Equal(t, "127.0.0.1", resolveNoiseHost(f))
}

// TestNoiseHost_FromConfigFile: bootstrap.noise_host overrides server.host.
func TestNoiseHost_FromConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "qrdb.yaml")
	body := `mode: "raft"
server:
  host: "127.0.0.1"
bootstrap:
  noise_host: "10.4.4.4"
raft:
  data_dir: "` + filepath.Join(dir, "data") + `"
  deployment_id: 1
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:19001"
  peers: "1=127.0.0.1:19001"
tls:
  enabled: false
logging:
  level: "info"
  format: "text"
discovery:
  method: "static"
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	f := resolveWithArgs(t, "--config", path)
	assert.Equal(t, "10.4.4.4", resolveNoiseHost(f))
}

// TestNoiseHost_FlagBeatsFile pins precedence for the enrollment bind host.
func TestNoiseHost_FlagBeatsFile(t *testing.T) {
	cfgPath := writeBindHostConfig(t, `  host: "127.0.0.1"`)
	f := resolveWithArgs(t, "--config", cfgPath, "--bootstrap-noise-host", "10.7.7.7")
	assert.Equal(t, "10.7.7.7", resolveNoiseHost(f))
}
