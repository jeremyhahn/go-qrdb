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

//go:build !integration

package node

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// sealedHost is a host that runs its own listeners and mounts qrdb's sealed
// surface on them through the SDK alone: the REST handler on its own mux next
// to a route of its own, and the MCP tool set on its own MCP server next to a
// tool of its own -- both gated by ONE probe, as the SDK documents.
type sealedHost struct {
	node *Node
	rest *httptest.Server
	mcp  *client.Client
}

func newSealedHost(t *testing.T, strategy string) *sealedHost {
	t.Helper()
	cfg := sealedStandaloneConfig(t, t.TempDir())
	cfg.Seal.Strategy = strategy
	if strategy == "shamir" {
		cfg.Seal.ShamirThreshold = 2
		cfg.Seal.ShamirTotal = 3
	}
	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	require.True(t, n.IsSealed())

	svc := n.Services()
	require.NotNil(t, svc, "a sealed node exposes its seal-layer services")
	// The ceremony signs officer CSRs with the node's ceremony CA, which
	// qrdbd sets on the ceremony core at boot (lifecycle.Infra.CeremonyCA);
	// the SDK node has no option for it, so the test sets it the same way.
	core, ok := service.CeremonyServiceCore(svc.Ceremony)
	require.True(t, ok)
	core.SetCA(testCeremonyCA(t))
	probe := sdk.NewSealedWindowListenerProbe(n.barrier.Load(), svc.Ceremony)

	mux := http.NewServeMux()
	sealed := sdk.NewSealedWindowHandler(svc.Barrier, svc.Ceremony, svc.Credential, probe)
	for _, prefix := range []string{"/v1/barrier/", "/v1/ceremony/", "/health"} {
		mux.Handle(prefix, sealed)
	}
	mux.HandleFunc("/host/own", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "host") })
	restSrv := httptest.NewServer(mux)
	t.Cleanup(restSrv.Close)

	mcpSrv := mcpserver.NewMCPServer("host", "1")
	mcpSrv.AddTools(sdk.SealedWindowMCPTools(svc.Barrier, svc.Ceremony, svc.Credential, probe)...)
	mcpSrv.AddTool(mcp.NewTool("host_own"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("host"), nil
	})
	c, err := client.NewInProcessClient(mcpSrv)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	require.NoError(t, c.Start(ctx))
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "host-test", Version: "1"}
	_, err = c.Initialize(ctx, init)
	require.NoError(t, err)

	return &sealedHost{node: n, rest: restSrv, mcp: c}
}

func (h *sealedHost) do(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, h.rest.URL+path, r)
	require.NoError(t, err)
	resp, err := h.rest.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, out
}

func (h *sealedHost) call(t *testing.T, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = tool
	req.Params.Arguments = args
	res, err := h.mcp.CallTool(context.Background(), req)
	require.NoError(t, err)
	return res
}

func resultText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// testCeremonyCA is a self-signed ECDSA CA for the ceremony to sign with.
func testCeremonyCA(t *testing.T) ceremony.CeremonyCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mount-test-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	ca, err := ceremony.NewLocalCA(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	require.NoError(t, err)
	return ca
}

func officerCSR(t *testing.T, cn string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: cn}, SignatureAlgorithm: x509.ECDSAWithSHA256,
	}, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

// TestSealedWindowMount_RESTServesTheSealedSurface: officer registration (the
// 404 a host-built listener used to answer), barrier status and the claim
// status reach qrdb's handlers through the host's mux; a verb off the
// allowlist and GET /health answer the sealed envelope; the host's own route
// is untouched.
func TestSealedWindowMount_RESTServesTheSealedSurface(t *testing.T) {
	h := newSealedHost(t, "shamir")

	code, body := h.do(t, http.MethodPost, "/v1/barrier/officers", map[string]string{"username": "off1", "csr_pem": officerCSR(t, "off1")})
	require.Equal(t, http.StatusOK, code, "officer register: %s", body)
	var reg struct {
		CertPEM    string `json:"cert_pem"`
		ShareIndex int    `json:"share_index"`
	}
	require.NoError(t, json.Unmarshal(body, &reg))
	require.Equal(t, 1, reg.ShareIndex)
	require.Contains(t, reg.CertPEM, "BEGIN CERTIFICATE")

	code, body = h.do(t, http.MethodGet, "/v1/barrier/officers/claim-status", nil)
	require.Equal(t, http.StatusOK, code, "claim status: %s", body)
	require.NotContains(t, string(body), "off1", "the anonymous claim status names no officer")

	code, body = h.do(t, http.MethodGet, "/v1/barrier/status", nil)
	require.Equal(t, http.StatusOK, code, "barrier status: %s", body)

	for _, path := range []string{"/v1/barrier/officers", "/health"} {
		code, body = h.do(t, http.MethodGet, path, nil)
		require.Equal(t, http.StatusServiceUnavailable, code, "GET %s while sealed: %s", path, body)
		require.Contains(t, string(body), `"code":"SEALED"`, "GET %s", path)
	}

	code, body = h.do(t, http.MethodGet, "/host/own", nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "host", string(body))
}

// TestSealedWindowMount_MCPToolsAreTheSealedAllowlist: the exported tool set
// is the sealed allowlist the services can serve -- officer registration and
// health included, nothing off the allowlist -- and each tool answers through
// qrdb's gate and handlers on the host's MCP server.
func TestSealedWindowMount_MCPToolsAreTheSealedAllowlist(t *testing.T) {
	h := newSealedHost(t, "shamir")

	listed, err := h.mcp.ListTools(context.Background(), mcp.ListToolsRequest{})
	require.NoError(t, err)
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"qrdb_barrier_officer_register", "qrdb_barrier_status", "qrdb_barrier_unseal", "qrdb_health", "qrdb_ceremony_status", "host_own"} {
		require.True(t, names[want], "tool %s missing from the host's server", want)
	}
	for name := range names {
		if name == "host_own" {
			continue
		}
		_, allowlisted := sdk.MCPSealedWindowAllowlist[name]
		require.True(t, allowlisted, "exported tool %s is not on the sealed allowlist", name)
	}

	res := h.call(t, "qrdb_barrier_officer_register", map[string]any{"username": "off1", "csr_pem": officerCSR(t, "off1")})
	require.False(t, res.IsError, "officer register: %s", resultText(res))
	require.Contains(t, resultText(res), "BEGIN CERTIFICATE")

	res = h.call(t, "qrdb_health", nil)
	require.False(t, res.IsError, "health: %s", resultText(res))
	require.Contains(t, resultText(res), `"sealed"`)
}

// TestSealedWindowMount_UnsealClosesEveryMountedTransport: the shared probe
// sees the barrier unsealed and both mounted surfaces refuse from then on --
// REST with the closed envelope, MCP with CodeUnavailable -- so nothing a host
// mounted keeps serving anonymously against an unsealed barrier.
func TestSealedWindowMount_UnsealClosesEveryMountedTransport(t *testing.T) {
	h := newSealedHost(t, "passphrase")

	code, _ := h.do(t, http.MethodGet, "/v1/barrier/status", nil)
	require.Equal(t, http.StatusOK, code)

	creds := seal.MustCredentials([]byte("mount-passphrase"))
	if err := h.node.barrier.Load().Initialize(context.Background(), creds); err != nil && !errors.Is(err, seal.ErrAlreadyInitialized) {
		t.Fatalf("barrier initialize: %v", err)
	}
	require.False(t, h.node.barrier.Load().IsSealed())

	code, body := h.do(t, http.MethodGet, "/v1/barrier/status", nil)
	require.Equal(t, http.StatusServiceUnavailable, code, "REST after unseal: %s", body)
	require.Contains(t, string(body), "SEALED_WINDOW_CLOSED")

	res := h.call(t, "qrdb_barrier_status", nil)
	require.True(t, res.IsError, "MCP after unseal answered: %s", resultText(res))
}
