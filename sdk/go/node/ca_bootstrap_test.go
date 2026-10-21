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
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/internal/netport"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// bootstrapNode boots a standalone node serving REST over TCP and HTTP/3 on
// pre-bound sockets -- the two transports that serve /v1/ca/bootstrap -- with
// extra server options, and returns its REST address and the client trust.
func bootstrapNode(t *testing.T, extra ...server.ServerOption) (string, *x509.CertPool, error) {
	t.Helper()
	tlsCfg, pool := preboundServerTLS(t)
	restLn, h3, err := netport.ListenPair("127.0.0.1:0")
	require.NoError(t, err, "bind the REST listener and HTTP/3 on one port")
	cfg := preboundNodeConfig(t)
	cfg.Protocols.GRPC = false
	opts := append([]server.ServerOption{
		server.WithTLS(tlsCfg),
		server.WithRESTListener(restLn),
		server.WithRESTPacketConn(h3),
	}, extra...)
	n, err := NewStandaloneNode(context.Background(), cfg, opts)
	if err != nil {
		return "", nil, err
	}
	t.Cleanup(func() { _ = n.Close() })
	return restLn.Addr().String(), pool, nil
}

// bootstrapGet fetches /v1/ca/bootstrap without a client certificate over
// HTTPS (h3=false) or HTTP/3 (h3=true).
func bootstrapGet(t *testing.T, pool *x509.CertPool, addr string, h3 bool) (int, []byte) {
	t.Helper()
	tlsCfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}
	var rt http.RoundTripper = &http.Transport{TLSClientConfig: tlsCfg}
	if h3 {
		h3rt := &http3.Transport{TLSClientConfig: tlsCfg}
		t.Cleanup(func() { _ = h3rt.Close() })
		rt = h3rt
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: rt}
	resp, err := client.Get("https://" + addr + "/v1/ca/bootstrap")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, body
}

// TestBootstrapCABundle_HostBundleServedOnEveryBootstrapTransport: a host's
// bundle supplied through sdk.WithBootstrapCABundle is what an unauthenticated
// client fetches over REST and over HTTP/3. qrdb owns the route; the host
// supplies only the data.
func TestBootstrapCABundle_HostBundleServedOnEveryBootstrapTransport(t *testing.T) {
	hostCA, err := qrdbdharness.NewCA(t.TempDir())
	require.NoError(t, err)
	addr, pool, err := bootstrapNode(t, sdk.WithBootstrapCABundle(func(context.Context) ([]byte, error) {
		return hostCA.CertPEM, nil
	}))
	require.NoError(t, err)
	for name, h3 := range map[string]bool{"https": false, "http3": true} {
		code, body := bootstrapGet(t, pool, addr, h3)
		require.Equal(t, http.StatusOK, code, "%s: %s", name, body)
		require.Equal(t, string(hostCA.CertPEM), string(body), "%s served another bundle", name)
	}
}

// TestBootstrapCABundle_UnconfiguredPathIsNotRegistered: with neither qrdb's
// cluster CA nor a host bundle, the path is not registered on either
// transport.
func TestBootstrapCABundle_UnconfiguredPathIsNotRegistered(t *testing.T) {
	addr, pool, err := bootstrapNode(t)
	require.NoError(t, err)
	for name, h3 := range map[string]bool{"https": false, "http3": true} {
		code, _ := bootstrapGet(t, pool, addr, h3)
		require.Equal(t, http.StatusNotFound, code, "%s", name)
	}
}

// TestBootstrapCABundle_ConflictIsRefused: qrdb's cluster CA and a host bundle
// together refuse the node with the typed error; neither is picked.
func TestBootstrapCABundle_ConflictIsRefused(t *testing.T) {
	clusterCA, err := qrdbdharness.NewCA(t.TempDir())
	require.NoError(t, err)
	_, _, err = bootstrapNode(t,
		server.WithCABootstrap(clusterCA.CertPEM),
		sdk.WithBootstrapCABundle(func(context.Context) ([]byte, error) { return clusterCA.CertPEM, nil }))
	var conflict *sdk.CABootstrapConflictError
	require.True(t, errors.As(err, &conflict), "err = %T (%v), want *sdk.CABootstrapConflictError", err, err)
}
