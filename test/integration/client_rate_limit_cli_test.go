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

//go:build integration

package integration_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// clientRateLimitBurst is large enough for the harness's own start-up
// traffic from 127.0.0.1 and small enough to drain in one test; the rate
// refills a token every thousand seconds, so nothing refills mid-test.
const clientRateLimitBurst = 400

// drainLoopbackBucket spends 127.0.0.1's bucket with unauthenticated REST
// requests, each naming a different client in an X-Forwarded-For header that
// an untrusted peer cannot make the limiter read, and returns the 429.
func drainLoopbackBucket(t *testing.T, roots *x509.CertPool, restPort int) *http.Response {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13},
	}}
	defer c.CloseIdleConnections()
	url := fmt.Sprintf("https://127.0.0.1:%d/health", restPort)
	for i := 0; i <= clientRateLimitBurst; i++ {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i%250+1))
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			return resp
		}
	}
	t.Fatalf("no 429 after %d requests from one address with varying X-Forwarded-For", clientRateLimitBurst+1)
	return nil
}

// TestClientRateLimit_CLI_PerProtocol: once 127.0.0.1 has spent its burst --
// over REST, with an X-Forwarded-For header the untrusted peer cannot use to
// change its key -- the CLI is refused with the rate-limit error over every
// TCP and UDP transport, because they share one limiter keyed by address,
// while the Unix socket's caller, a different client, is still served.
func TestClientRateLimit_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	sqlPorts := allocateTCPPorts(t, 2)
	node := startMultiProtocolNodeWithConfig(t, "", fmt.Sprintf(
		"transport:\n  client_rate_limit:\n    rate_per_second: 0.001\n    burst: %d\n", clientRateLimitBurst),
		"--enable-mysql", "--mysql-port", fmt.Sprintf("%d", sqlPorts[0]),
		"--enable-pgsql", "--pgsql-port", fmt.Sprintf("%d", sqlPorts[1]))

	resp := drainLoopbackBucket(t, node.ca.Pool, node.restPort)
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("429 carries no Retry-After")
	}

	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			out, err := runCLIProto(t, node, proto, "kv", "exists", "rate-limit-probe")
			if proto.name == "unix" {
				if err != nil {
					t.Fatalf("unix caller refused: %v\n%s", err, out)
				}
				return
			}
			if err == nil {
				t.Fatalf("drained address served over %s:\n%s", proto.name, out)
			}
			if !strings.Contains(strings.ToLower(out), "rate limit") {
				t.Fatalf("refusal over %s does not name the rate limit:\n%s", proto.name, out)
			}
		})
	}

	// The SQL wire listeners share the node's bucket: a connection from the
	// drained address is refused before authentication, the way each
	// protocol signals it.
	tlsCfg := &tls.Config{
		RootCAs:      node.ca.Pool,
		Certificates: []tls.Certificate{loadTLSCert(t, node.clientCert, node.clientKey)},
		MinVersion:   tls.VersionTLS13,
	}
	t.Run("mysql", func(t *testing.T) {
		tlsName := fmt.Sprintf("client-rate-limit-%d", sqlPorts[0])
		if err := gomysql.RegisterTLSConfig(tlsName, tlsCfg); err != nil {
			t.Fatalf("RegisterTLSConfig: %v", err)
		}
		t.Cleanup(func() { gomysql.DeregisterTLSConfig(tlsName) })
		db, err := sql.Open("mysql", fmt.Sprintf("admin@tcp(127.0.0.1:%d)/system?tls=%s", sqlPorts[0], tlsName))
		if err != nil {
			t.Fatalf("sql.Open: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err = db.PingContext(ctx)
		var myErr *gomysql.MySQLError
		if !errors.As(err, &myErr) {
			t.Fatalf("drained address: want an ERR packet, got %v", err)
		}
		// ER_USER_LIMIT_REACHED, SQLSTATE 42000.
		if myErr.Number != 1226 || string(myErr.SQLState[:]) != "42000" || !strings.Contains(myErr.Message, "rate limit") {
			t.Fatalf("drained address: want ERR 1226 (42000) naming the rate limit, got %v", myErr)
		}
	})
	t.Run("postgresql", func(t *testing.T) {
		cfg, err := pgx.ParseConfig(fmt.Sprintf("host=127.0.0.1 port=%d dbname=qrdb user=admin sslmode=require", sqlPorts[1]))
		if err != nil {
			t.Fatalf("ParseConfig: %v", err)
		}
		cfg.TLSConfig = tlsCfg.Clone()
		cfg.TLSConfig.ServerName = "localhost"
		cfg.Fallbacks = nil
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err == nil {
			_ = conn.Close(context.Background())
			t.Fatal("drained address connected over PostgreSQL")
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) {
			t.Fatalf("drained address: want an ErrorResponse, got %v", err)
		}
		// FATAL sqlserver_rejected_establishment_of_sqlconnection.
		if pgErr.Code != "08004" || pgErr.Severity != "FATAL" || !strings.Contains(pgErr.Message, "rate limit") {
			t.Fatalf("drained address: want FATAL 08004 naming the rate limit, got %v", pgErr)
		}
	})
}

// TestClientRateLimit_Etcd drains 127.0.0.1's bucket over REST on a node
// built with the etcd v3 listener and checks an etcd client from the same
// address is refused with ResourceExhausted before it is authorized: the
// etcd listener is its own gRPC server and shares the node's one bucket.
func TestClientRateLimit_Etcd(t *testing.T) {
	t.Parallel()
	cfgPath := filepath.Join(t.TempDir(), "qrdbd.yaml")
	cfg := fmt.Sprintf("transport:\n  client_rate_limit:\n    rate_per_second: 0.001\n    burst: %d\n", clientRateLimitBurst)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	adminPort, etcdPort, ca := startSingleNodeWithEtcd(t, "--config", cfgPath)

	drainLoopbackBucket(t, ca.Pool, adminPort)

	cli := newEtcdClient(t, ca, etcdPort)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := cli.Get(ctx, "rate-limit-probe")
	if err == nil {
		t.Fatal("drained address served over etcd")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.ResourceExhausted || !strings.Contains(st.Message(), "rate limit") {
		t.Fatalf("drained address: want ResourceExhausted naming the rate limit, got %v", err)
	}
}
