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

package node

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/clienttrust"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// composerCertProvider is an embedder's own login CA.
type composerCertProvider struct{}

var _ cert.CertProvider = composerCertProvider{}

func (composerCertProvider) IssueClientCert(context.Context, *cert.IssueRequest) (*cert.IssuedCert, error) {
	return nil, errors.New("composerCertProvider: not used")
}

func (composerCertProvider) RevokeCert(context.Context, string) (*cert.RevokeResult, error) {
	return nil, errors.New("composerCertProvider: not used")
}

func (composerCertProvider) IsRevoked(context.Context, string) (bool, error) { return false, nil }

func (composerCertProvider) TenantForSerial(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func (composerCertProvider) ListCerts(_ context.Context, tenantID string) ([]*cert.CertRecord, error) {
	return []*cert.CertRecord{{TenantID: tenantID, Username: "composer"}}, nil
}

// countingKeyFactory is a Key B factory that backs no tenant and counts how
// often the login CA enumerates it.
type countingKeyFactory struct{ enumerated atomic.Int32 }

var _ cakey.ProviderFactory = (*countingKeyFactory)(nil)

func (*countingKeyFactory) ProviderForTenant(string) (cakey.Provider, error) {
	return nil, &cakey.NotInitializedError{}
}

func (f *countingKeyFactory) TenantIDs(context.Context) ([]string, error) {
	f.enumerated.Add(1)
	return nil, nil
}

func TestWithCertProvider_ReplacesTheNodesLoginCA(t *testing.T) {
	n := newSeamNode(t, nil, WithCertProvider(composerCertProvider{}))
	require.IsType(t, composerCertProvider{}, n.Services().CertProv)
	recs, err := n.Services().CertProv.ListCerts(context.Background(), "acme")
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, "composer", recs[0].Username)
}

func TestWithCertProvider_AbsentKeepsTheBuiltInLoginCA(t *testing.T) {
	n := newSeamNode(t, nil)
	require.IsType(t, &cert.DefaultCertProvider{}, n.Services().CertProv)
}

func TestWithCertKeyProviderFactory_ReachesTheBuiltInLoginCA(t *testing.T) {
	f := &countingKeyFactory{}
	n := newSeamNode(t, nil, WithCertKeyProviderFactory(f))
	src, ok := n.Services().CertProv.(clienttrust.CertSource)
	require.True(t, ok, "the built-in login CA is a CertSource")
	_, err := src.CACertPEMs(context.Background())
	require.NoError(t, err)
	require.Positive(t, f.enumerated.Load(), "the login CA never enumerated the composer's factory")
}

func TestWithCertKeyProviderFactory_AndWithCertProviderConflict(t *testing.T) {
	cfg := newTenantIsolationConfig(t)
	n, err := NewStandaloneNode(context.Background(), cfg, nil,
		WithCertProvider(composerCertProvider{}), WithCertKeyProviderFactory(&countingKeyFactory{}))
	if n != nil {
		_ = n.Close()
	}
	var conflict *lifecycle.CertProviderConflictError
	require.ErrorAs(t, err, &conflict)
}

func TestMergeSeamOptions_FoldsTheCertSeams(t *testing.T) {
	f := &countingKeyFactory{}
	o := &nodeOptions{}
	WithCertKeyProviderFactory(f)(o)
	WithCertProvider(composerCertProvider{})(o)

	got := server.ResolveCryptoOptions(mergeSeamOptions(*o, nil)...)
	require.Same(t, f, got.CertKeyProviderFactory)
	require.Equal(t, composerCertProvider{}, got.CertProvider)
}

func TestMergeSeamOptions_UnsetCertProviderLeavesTheBuiltInOnTheServer(t *testing.T) {
	n := newSeamNode(t, nil)
	builtIn := n.Services().CertProv
	require.IsType(t, &cert.DefaultCertProvider{}, builtIn)

	// The merged seams reach buildServerOpts as caller options, which win;
	// an unset cert seam must not take the built-in login CA off the server.
	opts := buildServerOpts(n.Services(), nil, nil, mergeSeamOptions(nodeOptions{}, nil), true)
	require.Same(t, builtIn, server.ResolveCryptoOptions(opts...).CertProvider)
}

func TestMergeSeamOptions_SetCertProviderReachesTheServer(t *testing.T) {
	n := newSeamNode(t, nil, WithCertProvider(composerCertProvider{}))
	o := &nodeOptions{}
	WithCertProvider(composerCertProvider{})(o)

	opts := buildServerOpts(n.Services(), nil, nil, mergeSeamOptions(*o, nil), true)
	require.Equal(t, composerCertProvider{}, server.ResolveCryptoOptions(opts...).CertProvider)
}

func TestWithCryptoInfra_ForwardsTheCertSeams(t *testing.T) {
	f := &countingKeyFactory{}
	infra, err := withCryptoInfra(lifecycle.Infra{}, nil, []server.ServerOption{
		server.WithCertKeyProviderFactory(f), server.WithCertProvider(composerCertProvider{}),
	})
	require.NoError(t, err)
	require.Same(t, f, infra.CertKeyProviderFactory)
	require.Equal(t, composerCertProvider{}, infra.CertProvider)

	infra, err = withCryptoInfra(lifecycle.Infra{}, nil, nil)
	require.NoError(t, err)
	require.Nil(t, infra.CertKeyProviderFactory)
	require.Nil(t, infra.CertProvider)
}
