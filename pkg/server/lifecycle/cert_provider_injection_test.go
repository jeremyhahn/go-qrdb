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

package lifecycle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// injectedCertProvider is an embedder's certificate provider. It answers
// ListCerts with its own marker so a test can tell it from the built-in one.
type injectedCertProvider struct{}

var _ cert.CertProvider = injectedCertProvider{}

func (injectedCertProvider) IssueClientCert(context.Context, *cert.IssueRequest) (*cert.IssuedCert, error) {
	return nil, errors.New("injectedCertProvider: not used")
}

func (injectedCertProvider) RevokeCert(context.Context, string) (*cert.RevokeResult, error) {
	return nil, errors.New("injectedCertProvider: not used")
}

func (injectedCertProvider) IsRevoked(context.Context, string) (bool, error) { return false, nil }

func (injectedCertProvider) TenantForSerial(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func (injectedCertProvider) ListCerts(_ context.Context, tenantID string) ([]*cert.CertRecord, error) {
	return []*cert.CertRecord{{Username: "injected:" + tenantID}}, nil
}

// emptyKeyFactory is a cakey.ProviderFactory that backs no tenant.
type emptyKeyFactory struct{}

func (emptyKeyFactory) ProviderForTenant(string) (cakey.Provider, error) {
	return nil, &cakey.NotInitializedError{}
}

func (emptyKeyFactory) TenantIDs(context.Context) ([]string, error) { return nil, nil }

// buildWithCertSeams builds the service graph with the given certificate seams.
func buildWithCertSeams(t *testing.T, prov cert.CertProvider, factory cakey.ProviderFactory) (*lifecycle.Node, error) {
	t.Helper()
	host := newLocalHost()
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)

	barrier, registry, credSvc := newTestBarrier(t)
	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:                   host,
		WatchHub:               hub,
		DataDir:                t.TempDir(),
		SealBarrier:            barrier,
		BarrierRegistry:        registry,
		CredentialCoreSvc:      credSvc,
		CertProvider:           prov,
		CertKeyProviderFactory: factory,
	})
	if err == nil {
		t.Cleanup(func() { _ = node.Stop(context.Background()) })
	}
	return node, err
}

func TestBuildServices_InjectedCertProviderReplacesTheBuiltIn(t *testing.T) {
	node, err := buildWithCertSeams(t, injectedCertProvider{}, nil)
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	if _, ok := node.CertProv.(injectedCertProvider); !ok {
		t.Fatalf("CertProv = %T, want the injected provider", node.CertProv)
	}
	if node.CertEnrollment == nil {
		t.Fatal("certificate enrollment was not built over the injected provider")
	}
	recs, err := node.CertProv.ListCerts(context.Background(), "acme")
	if err != nil || len(recs) != 1 || recs[0].Username != "injected:acme" {
		t.Fatalf("ListCerts = %+v, %v; want the injected provider's answer", recs, err)
	}
}

func TestBuildServices_NoInjectedCertProviderBuildsTheDefault(t *testing.T) {
	node, err := buildWithCertSeams(t, nil, nil)
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	if _, ok := node.CertProv.(*cert.DefaultCertProvider); !ok {
		t.Fatalf("CertProv = %T, want *cert.DefaultCertProvider", node.CertProv)
	}
}

func TestBuildServices_CertProviderAndKeyFactoryConflict(t *testing.T) {
	_, err := buildWithCertSeams(t, injectedCertProvider{}, emptyKeyFactory{})
	var conflict *lifecycle.CertProviderConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("BuildServices error = %v, want *CertProviderConflictError", err)
	}
	if conflict.Error() == "" {
		t.Fatal("the conflict error has no message")
	}
}
