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
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
)

func TestNodeIdentityCmd_PrintsCertificateIdentity(t *testing.T) {
	bundle, err := generateCATLS()
	require.NoError(t, err)
	certFile := filepath.Join(t.TempDir(), "node.pem")
	require.NoError(t, os.WriteFile(certFile, bundle.CertPEM, 0o600))

	want, err := cluster.MemberIdentityFromPEM(bundle.CertPEM)
	require.NoError(t, err)

	cmd := newNodeIdentityCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--cert", certFile})
	require.NoError(t, cmd.Execute())
	require.Equal(t, want+"\n", out.String())
}

func TestNodeIdentityCmd_Errors(t *testing.T) {
	notPEM := filepath.Join(t.TempDir(), "junk.pem")
	require.NoError(t, os.WriteFile(notPEM, []byte("not a certificate"), 0o600))

	tests := []struct {
		name string
		args []string
		want func(error) bool
	}{
		{name: "missing file", args: []string{"--cert", filepath.Join(t.TempDir(), "absent.pem")}, want: func(err error) bool {
			var ce *cluster.MemberIdentityCertificateError
			return errors.As(err, &ce) && errors.Is(err, os.ErrNotExist)
		}},
		{name: "not a certificate", args: []string{"--cert", notPEM}, want: func(err error) bool {
			var ce *cluster.MemberIdentityCertificateError
			return errors.As(err, &ce)
		}},
		{name: "flag required", args: nil, want: func(err error) bool { return err != nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newNodeIdentityCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tc.args)
			err := cmd.Execute()
			require.True(t, tc.want(err), "unexpected error: %v", err)
		})
	}
}

// multiNodeIdentityFlags is a two-replica static cluster whose OTHER replica
// never runs: enough for StartShard to decide whether the founding membership
// names an identity for every member.
func multiNodeIdentityFlags(t *testing.T, memberIdentities []string) *startFlags {
	t.Helper()
	port, err := freePortOn(testBindHost, networkUDP)
	require.NoError(t, err)
	addr := "127.0.0.1:" + strconv.Itoa(port)
	return &startFlags{
		smType:           "memory",
		discoveryMethod:  "static",
		peers:            []string{"1=" + addr, "2=127.0.0.2:63001"},
		memberIdentities: memberIdentities,
		dataDir:          t.TempDir(),
		listen:           addr,
		raftAddress:      addr,
		shardID:          1,
		replicaID:        1,
		deploymentID:     1,
		logFormat:        "text",
		logLevel:         "error",
		host:             testBindHost,
	}
}

// peerTwoIdentity is a well-formed identity for the replica that never runs.
var peerTwoIdentity = "spki:" + strings.Repeat("2b", 32)

// TestInitializeNode_MemberIdentitiesFormTheShard is go-qrdb#556 on the
// daemon path: with the peer's identity supplied and its own derived from the
// transport certificate, the founding StartShard is accepted.
func TestInitializeNode_MemberIdentitiesFormTheShard(t *testing.T) {
	bundle, err := generateCATLS()
	require.NoError(t, err)
	f := multiNodeIdentityFlags(t, []string{"2=" + peerTwoIdentity})

	host, err := initializeNode(context.Background(), f, bundle)
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.Close() })

	self, err := cluster.MemberIdentityFromPEM(bundle.CertPEM)
	require.NoError(t, err)
	require.Equal(t, self, host.NodeIdentity(),
		"the identity derived from the certificate must be the one the transport presents")
}

// TestInitializeNode_MissingPeerIdentityRefused pins the refusal the daemon
// used to hit on every multi-node start: no identity for replica 2.
func TestInitializeNode_MissingPeerIdentityRefused(t *testing.T) {
	bundle, err := generateCATLS()
	require.NoError(t, err)
	f := multiNodeIdentityFlags(t, nil)

	host, err := initializeNode(context.Background(), f, bundle)
	if err == nil {
		_ = host.Close()
	}
	var invalid *quicraft.InvalidMemberError
	require.ErrorAs(t, err, &invalid)
}

// TestInitializeNode_BadMemberIdentityRefusedBeforeHost pins that a malformed
// or contradicting identity is refused before any host is built.
func TestInitializeNode_BadMemberIdentityRefusedBeforeHost(t *testing.T) {
	bundle, err := generateCATLS()
	require.NoError(t, err)

	t.Run("malformed", func(t *testing.T) {
		f := multiNodeIdentityFlags(t, []string{"2=not-an-identity"})
		_, err := initializeNode(context.Background(), f, bundle)
		var formatErr *cluster.MemberIdentityFormatError
		require.ErrorAs(t, err, &formatErr)
		_, statErr := os.Stat(filepath.Join(f.dataDir, "wal"))
		require.ErrorIs(t, statErr, os.ErrNotExist, "no host directory may be created for a refused configuration")
	})
	t.Run("contradicts the certificate", func(t *testing.T) {
		f := multiNodeIdentityFlags(t, []string{"1=" + peerTwoIdentity, "2=" + peerTwoIdentity})
		_, err := initializeNode(context.Background(), f, bundle)
		var mismatch *cluster.MemberIdentityMismatchError
		require.ErrorAs(t, err, &mismatch)
	})
}
