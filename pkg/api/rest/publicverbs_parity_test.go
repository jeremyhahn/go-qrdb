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

//go:build !integration

package rest

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/publicverbs"
	"github.com/jeremyhahn/go-qrdb/pkg/api/publicverbs/publicverbstest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestPublicVerbsParity_REST fails when a publicverbs.Table row is not a
// public REST route. The other direction -- no route outside the table and
// the node-plane exemptions is public -- is
// TestRouteMappingGuard_ExemptRoutesAreExactlyDeclared, which takes its
// client verbs from the same table.
func TestPublicVerbsParity_REST(t *testing.T) {
	srv := newFullyWiredGuardServer(t)
	for _, v := range publicverbs.Verbs {
		t.Run(string(v), func(t *testing.T) {
			method, path := publicverbs.RESTRoute(v)
			if _, kind, ok := srv.routePerms.Lookup(method, path); !ok || kind != authmw.RESTRoutePublic {
				t.Errorf("%s %s (%s): ok=%v kind=%v, want RESTRoutePublic", method, path, v, ok, kind)
			}
		})
	}
}

// TestPublicVerbsParity_RESTHealthAnswer: a caller with no certificate gets
// the admin service's Health answer, every field intact, the same fixture
// the gRPC, QUIC and Unix parity tests compare against.
func TestPublicVerbsParity_RESTHealthAnswer(t *testing.T) {
	tlsCfg, pool := generateTestTLSConfig(t)
	serverCfg := tlsCfg.Clone()
	serverCfg.ClientAuth = tls.VerifyClientCertIfGiven
	serverCfg.ClientCAs = pool
	srv := NewServer(&mockKVService{}, publicverbstest.Admin{AdminServicer: &mockAdminService{}},
		dualStackAddrOn(t, "127.0.0.1"), WithTLS(serverCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	_, path := publicverbs.RESTRoute(publicverbs.VerbHealth)
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS13}}
	t.Cleanup(tr.CloseIdleConnections)
	resp, err := (&http.Client{Transport: tr, Timeout: 10 * time.Second}).Get("https://" + srv.Addr() + path)
	if err != nil {
		t.Fatalf("GET %s with no certificate: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s with no certificate = %d, want 200", path, resp.StatusCode)
	}
	var got healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	publicverbstest.CheckHealth(t, "REST", service.HealthStatus{
		Status: got.Status, Leader: got.IsLeader, DiagnosticsDropped: got.DiagnosticsDropped,
	})
}
