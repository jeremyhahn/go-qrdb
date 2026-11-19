// Copyright 2026 The go-qrdb Authors
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

//go:build !integration

package node

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// registerSealedOfficer enrols one share holder through the sealed node's
// ceremony service -- the service a shamir-sealed embedder's officers
// register through before the first unseal -- and returns the issued
// certificate PEM.
func registerSealedOfficer(t *testing.T, n *Node, username string) []byte {
	t.Helper()
	require.True(t, n.IsSealed(), "the node must still be sealed")
	svc := n.Services()
	require.NotNil(t, svc, "a sealed node exposes its seal-layer services")
	core, ok := service.CeremonyServiceCore(svc.Ceremony)
	require.True(t, ok, "the sealed ceremony service wraps a ceremony core")
	res, err := core.RegisterOfficer(context.Background(), seal.DomainUnseal, ceremony.RegisterOfficerRequest{
		Username: username,
		CSRPEM:   []byte(officerCSR(t, username)),
	})
	require.NoError(t, err, "officer registration while sealed")
	require.Contains(t, string(res.CertPEM), "BEGIN CERTIFICATE")
	return res.CertPEM
}

// TestNewStandaloneNode_SealedCeremonyIssuesFromWithCeremonyCA: a
// shamir-sealed standalone node given server.WithCeremonyCA registers an
// officer while sealed, its certificate issued by that CA. Without the CA
// reaching the sealed ceremony service the registration is refused with
// ceremony.ErrCANotInitialized.
func TestNewStandaloneNode_SealedCeremonyIssuesFromWithCeremonyCA(t *testing.T) {
	cfg := sealedStandaloneConfig(t, t.TempDir())
	cfg.Seal.Strategy = "shamir"
	cfg.Seal.ShamirThreshold = 2
	cfg.Seal.ShamirTotal = 3
	ca := testCeremonyCA(t)
	n, err := NewStandaloneNode(context.Background(), cfg, []server.ServerOption{server.WithCeremonyCA(ca)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	requireIssuedBy(t, ca, registerSealedOfficer(t, n, "off1"))
}

// TestNewStandaloneNode_SealedCeremonyWithoutCARefuses pins the control: with
// no ceremony CA the sealed registration is the typed refusal.
func TestNewStandaloneNode_SealedCeremonyWithoutCARefuses(t *testing.T) {
	cfg := sealedStandaloneConfig(t, t.TempDir())
	cfg.Seal.Strategy = "shamir"
	cfg.Seal.ShamirThreshold = 2
	cfg.Seal.ShamirTotal = 3
	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	core, ok := service.CeremonyServiceCore(n.Services().Ceremony)
	require.True(t, ok)
	_, err = core.RegisterOfficer(context.Background(), seal.DomainUnseal, ceremony.RegisterOfficerRequest{
		Username: "off1",
		CSRPEM:   []byte(officerCSR(t, "off1")),
	})
	require.ErrorIs(t, err, ceremony.ErrCANotInitialized)
}

// TestNewClusteredNode_SealedCeremonyIssuesFromWithCeremonyCA is the
// clustered constructor's counterpart: its sealed-window ceremony service
// carries the WithCeremonyCA CA too.
func TestNewClusteredNode_SealedCeremonyIssuesFromWithCeremonyCA(t *testing.T) {
	ports, err := qrdbdharness.ReserveUDPPorts(1)
	require.NoError(t, err)
	r := formationReplica{id: 1, addr: "127.0.0.1:" + strconv.Itoa(ports[0])}
	cfg := sealClustered(formationConfig(t, r, ""))
	cfg.Seal.Strategy = "shamir"
	cfg.Seal.ShamirThreshold = 2
	cfg.Seal.ShamirTotal = 3
	ca := testCeremonyCA(t)
	n, err := NewClusteredNode(context.Background(), cfg, map[uint64]string{1: r.addr},
		[]server.ServerOption{server.WithCeremonyCA(ca)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	requireIssuedBy(t, ca, registerSealedOfficer(t, n, "off1"))
}

// requireIssuedBy asserts certPEM verifies under ca's certificate.
func requireIssuedBy(t *testing.T, ca ceremony.CeremonyCA, certPEM []byte) {
	t.Helper()
	caCert, err := ca.CACertificate(context.Background())
	require.NoError(t, err)
	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block, "the issued certificate is PEM")
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	require.NoError(t, cert.CheckSignatureFrom(caCert), "the officer certificate is issued by the WithCeremonyCA CA")
}
