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

package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// writeTrustDomainConfig writes a startable config whose auth section is
// authBody.
func writeTrustDomainConfig(t *testing.T, authBody string) string {
	t.Helper()
	return writeBindHostConfig(t, `  host: "127.0.0.1"
  admin_port: 19443
auth:
`+authBody)
}

func TestResolveStartFlags_ClusterTrustDomainDefaultsToTheClusterDefault(t *testing.T) {
	f := resolveWithArgs(t)
	assert.Equal(t, qrdbconfig.DefaultClusterTrustDomain, f.clusterTrustDomain)
}

func TestResolveStartFlags_ClusterTrustDomainFollowsTheFlag(t *testing.T) {
	f := resolveWithArgs(t, "--cluster-trust-domain", "flag.example.com")
	assert.Equal(t, "flag.example.com", f.clusterTrustDomain)
}

func TestResolveStartFlags_ClusterTrustDomainFollowsTheConfig(t *testing.T) {
	cfgPath := writeTrustDomainConfig(t, `  cluster_trust_domain: "cluster.example.com"`)
	f := resolveWithArgs(t, "--config", cfgPath)
	assert.Equal(t, "cluster.example.com", f.clusterTrustDomain)
}

func TestResolveStartFlags_ClusterFlagOverridesTheConfig(t *testing.T) {
	cfgPath := writeTrustDomainConfig(t, `  cluster_trust_domain: "cluster.example.com"`)
	f := resolveWithArgs(t, "--config", cfgPath, "--cluster-trust-domain", "flag.example.com")
	assert.Equal(t, "flag.example.com", f.clusterTrustDomain)
}

// auth.spiffe_trust_domain was folded into auth.cluster_trust_domain and
// deleted: a config still carrying it fails the way any unknown key does.
func TestResolveStartFlags_SPIFFETrustDomainKeyIsRefused(t *testing.T) {
	cfgPath := writeTrustDomainConfig(t, `  spiffe_trust_domain: "tenants.example.com"`)
	cmd, f := newStartCmdWithFlags()
	require.NoError(t, cmd.ParseFlags([]string{"--config", cfgPath}))
	err := resolveStartFlags(cmd, f)
	var ue *qrdbconfig.UnknownFieldError
	require.True(t, errors.As(err, &ue), "resolveStartFlags = %v, want an UnknownFieldError", err)
	assert.Equal(t, "auth.spiffe_trust_domain", ue.Path)
}

func TestResolveStartFlags_UnusableClusterFlagIsRefused(t *testing.T) {
	cmd, f := newStartCmdWithFlags()
	require.NoError(t, cmd.ParseFlags([]string{"--cluster-trust-domain", "bad/domain"}))
	err := resolveStartFlags(cmd, f)
	var ve *qrdbconfig.ValidationError
	require.True(t, errors.As(err, &ve), "resolveStartFlags = %v, want a ValidationError", err)
	assert.Equal(t, "auth.cluster_trust_domain", ve.Field)
}
