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

package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/entity"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// newDiscardLogger returns a slog.Logger that discards all output.
func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------------------------------------------------------------------------
// errors.go: BarrierRegistryError (lines 452-458) - not covered yet
// ---------------------------------------------------------------------------

func TestBarrierRegistryError_Error(t *testing.T) {
	inner := errors.New("missing key")
	e := &BarrierRegistryError{Err: inner}
	assert.Equal(t, "cli: barrier registry initialization failed: missing key", e.Error())
}

func TestBarrierRegistryError_Unwrap(t *testing.T) {
	inner := errors.New("unlock failed")
	e := &BarrierRegistryError{Err: inner}
	assert.Equal(t, inner, e.Unwrap())
	assert.True(t, errors.Is(e, inner))
}

// ---------------------------------------------------------------------------
// errors.go: ClusterDEKError (lines 469-475) - not covered yet
// ---------------------------------------------------------------------------

func TestClusterDEKError_Error(t *testing.T) {
	inner := errors.New("key derivation failed")
	e := &ClusterDEKError{Err: inner}
	assert.Equal(t, "cli: cluster DEK manager initialization failed: key derivation failed", e.Error())
}

func TestClusterDEKError_Unwrap(t *testing.T) {
	inner := errors.New("storage unavailable")
	e := &ClusterDEKError{Err: inner}
	assert.Equal(t, inner, e.Unwrap())
	assert.True(t, errors.Is(e, inner))
}

// ---------------------------------------------------------------------------
// errors.go: DEKStorageError (lines 485-491) - not covered yet
// ---------------------------------------------------------------------------

func TestDEKStorageError_Error(t *testing.T) {
	inner := errors.New("pebble open failed")
	e := &DEKStorageError{Err: inner}
	assert.Equal(t, "cli: DEK storage initialization failed: pebble open failed", e.Error())
}

func TestDEKStorageError_Unwrap(t *testing.T) {
	inner := errors.New("permission denied")
	e := &DEKStorageError{Err: inner}
	assert.Equal(t, inner, e.Unwrap())
	assert.True(t, errors.Is(e, inner))
}

// ---------------------------------------------------------------------------
// errors.go: TenantDEKError (lines 501-507) - not covered yet
// ---------------------------------------------------------------------------

func TestTenantDEKError_Error(t *testing.T) {
	inner := errors.New("tenant key missing")
	e := &TenantDEKError{Err: inner}
	assert.Equal(t, "cli: tenant DEK manager initialization failed: tenant key missing", e.Error())
}

func TestTenantDEKError_Unwrap(t *testing.T) {
	inner := errors.New("kek not found")
	e := &TenantDEKError{Err: inner}
	assert.Equal(t, inner, e.Unwrap())
	assert.True(t, errors.Is(e, inner))
}

// ---------------------------------------------------------------------------
// kvstore_adapter.go: RegisterEntityIndexes (lines 111-116) - not covered yet
// ---------------------------------------------------------------------------

// mockEntityRegisterer satisfies kvstore.EntityRegisterer for unit tests.
type mockEntityRegisterer struct {
	registerFn func(entityType string, indexes []kvstore.EntityIndex) error
}

func (m *mockEntityRegisterer) RegisterEntityType(entityType string, indexes []kvstore.EntityIndex) error {
	if m.registerFn != nil {
		return m.registerFn(entityType, indexes)
	}
	return nil
}

func TestKVServiceAdapter_RegisterEntityIndexes_WithRegisterer(t *testing.T) {
	var capturedType string
	var capturedIndexes []kvstore.EntityIndex

	registerer := &mockEntityRegisterer{
		registerFn: func(entityType string, indexes []kvstore.EntityIndex) error {
			capturedType = entityType
			capturedIndexes = indexes
			return nil
		},
	}
	mock := &mockKVServicer{}
	adapter := newKVServiceAdapter(mock, registerer, noopLookup)

	indexes := []kvstore.EntityIndex{
		{FieldName: "Email", JSONFieldName: "email"},
	}
	err := adapter.RegisterEntityIndexes("user", indexes)
	require.NoError(t, err)
	assert.Equal(t, "user", capturedType)
	assert.Equal(t, indexes, capturedIndexes)
}

// TestKVServiceAdapter_RegisterEntityIndexes_NilRegisterer pins the typed refusal.
//
// It previously asserted a nil error and an empty result — the (nil, nil)
// answer the KVStore contract now forbids, because it is indistinguishable
// from a maintained index with no matches. A DAO over this adapter would have
// reported "no records" for a lookup it never performed.
func TestKVServiceAdapter_RegisterEntityIndexes_NilRegisterer(t *testing.T) {
	mock := &mockKVServicer{}
	adapter := newKVServiceAdapter(mock, nil, nil)

	err := adapter.RegisterEntityIndexes("user", []kvstore.EntityIndex{{FieldName: "Email", JSONFieldName: "email"}})
	require.ErrorIs(t, err, kvstore.ErrIndexUnsupported,
		"with no registerer this adapter maintains no indexes and must say so")
}

func TestKVServiceAdapter_RegisterEntityIndexes_RegistrarError(t *testing.T) {
	sentinel := errors.New("duplicate index")
	registerer := &mockEntityRegisterer{
		registerFn: func(_ string, _ []kvstore.EntityIndex) error {
			return sentinel
		},
	}
	mock := &mockKVServicer{}
	adapter := newKVServiceAdapter(mock, registerer, noopLookup)

	err := adapter.RegisterEntityIndexes("user", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
}

// ---------------------------------------------------------------------------
// kvstore_adapter.go: QueryIndex (lines 120-136) - not covered yet
// ---------------------------------------------------------------------------

// TestKVServiceAdapter_QueryIndex_NilLookup pins the typed refusal.
//
// It previously asserted a nil error and an empty result — the (nil, nil)
// answer the KVStore contract now forbids, because it is indistinguishable
// from a maintained index with no matches. A DAO over this adapter would have
// reported "no records" for a lookup it never performed.
func TestKVServiceAdapter_QueryIndex_NilLookup(t *testing.T) {
	adapter := newKVServiceAdapter(&mockKVServicer{}, nil, nil)

	got, err := adapter.QueryIndex(context.Background(), "user.Email", []byte("a@example.com"))
	require.ErrorIs(t, err, kvstore.ErrIndexUnsupported)
	require.Nil(t, got, "a refused query carries no results")
}

func TestKVServiceAdapter_QueryIndex_WithLookup(t *testing.T) {
	expectedKeys := [][]byte{[]byte("user/1"), []byte("user/2")}
	lookup := kvstore.LookupFunc(func(_ context.Context, req interface{}) (interface{}, error) {
		qr, ok := req.(index.IndexQueryRequest)
		require.True(t, ok)
		assert.Equal(t, "email", qr.IndexName)
		assert.Equal(t, []byte("alice@example.com"), qr.Value)
		return expectedKeys, nil
	})
	mock := &mockKVServicer{}
	adapter := newKVServiceAdapter(mock, nil, lookup)

	result, err := adapter.QueryIndex(context.Background(), "email", []byte("alice@example.com"))
	require.NoError(t, err)
	assert.Equal(t, expectedKeys, result)
}

func TestKVServiceAdapter_QueryIndex_LookupError(t *testing.T) {
	sentinel := errors.New("index not found")
	lookup := kvstore.LookupFunc(func(_ context.Context, _ interface{}) (interface{}, error) {
		return nil, sentinel
	})
	mock := &mockKVServicer{}
	adapter := newKVServiceAdapter(mock, nil, lookup)

	result, err := adapter.QueryIndex(context.Background(), "email", []byte("x"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
	assert.Nil(t, result)
}

func TestKVServiceAdapter_QueryIndex_UnexpectedResultType(t *testing.T) {
	// Return wrong type to trigger the IndexQueryError branch.
	lookup := kvstore.LookupFunc(func(_ context.Context, _ interface{}) (interface{}, error) {
		return "not-a-slice-of-byte-slices", nil
	})
	mock := &mockKVServicer{}
	adapter := newKVServiceAdapter(mock, nil, lookup)

	result, err := adapter.QueryIndex(context.Background(), "myindex", []byte("val"))
	require.Error(t, err)
	var qErr *kvstore.IndexQueryError
	require.True(t, errors.As(err, &qErr))
	assert.Equal(t, "myindex", qErr.IndexName)
	assert.Nil(t, result)
}

// ---------------------------------------------------------------------------
// kvstore_adapter.go: ScanIndex (lines 140-157) - not covered yet
// ---------------------------------------------------------------------------

// TestKVServiceAdapter_ScanIndex_NilLookup pins the typed refusal.
//
// It previously asserted a nil error and an empty result — the (nil, nil)
// answer the KVStore contract now forbids, because it is indistinguishable
// from a maintained index with no matches. A DAO over this adapter would have
// reported "no records" for a lookup it never performed.
func TestKVServiceAdapter_ScanIndex_NilLookup(t *testing.T) {
	adapter := newKVServiceAdapter(&mockKVServicer{}, nil, nil)

	got, err := adapter.ScanIndex(context.Background(), "user.Email", []byte("a"), []byte("z"))
	require.ErrorIs(t, err, kvstore.ErrIndexUnsupported)
	require.Nil(t, got, "a refused scan carries no results")
}

func TestKVServiceAdapter_ScanIndex_WithLookup(t *testing.T) {
	expected := map[string][][]byte{
		"20": {[]byte("user/1")},
		"30": {[]byte("user/2")},
	}
	lookup := kvstore.LookupFunc(func(_ context.Context, req interface{}) (interface{}, error) {
		sr, ok := req.(index.IndexScanRequest)
		require.True(t, ok)
		assert.Equal(t, "age", sr.IndexName)
		assert.Equal(t, []byte("18"), sr.Start)
		assert.Equal(t, []byte("65"), sr.End)
		return expected, nil
	})
	mock := &mockKVServicer{}
	adapter := newKVServiceAdapter(mock, nil, lookup)

	result, err := adapter.ScanIndex(context.Background(), "age", []byte("18"), []byte("65"))
	require.NoError(t, err)
	assert.Equal(t, expected, result)
}

func TestKVServiceAdapter_ScanIndex_LookupError(t *testing.T) {
	sentinel := errors.New("scan failed")
	lookup := kvstore.LookupFunc(func(_ context.Context, _ interface{}) (interface{}, error) {
		return nil, sentinel
	})
	mock := &mockKVServicer{}
	adapter := newKVServiceAdapter(mock, nil, lookup)

	result, err := adapter.ScanIndex(context.Background(), "age", []byte("0"), []byte("100"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
	assert.Nil(t, result)
}

func TestKVServiceAdapter_ScanIndex_UnexpectedResultType(t *testing.T) {
	// Return wrong type to trigger the IndexScanError branch.
	lookup := kvstore.LookupFunc(func(_ context.Context, _ interface{}) (interface{}, error) {
		return 42, nil
	})
	mock := &mockKVServicer{}
	adapter := newKVServiceAdapter(mock, nil, lookup)

	result, err := adapter.ScanIndex(context.Background(), "age", []byte("0"), []byte("100"))
	require.Error(t, err)
	var sErr *kvstore.IndexScanError
	require.True(t, errors.As(err, &sErr))
	assert.Equal(t, "age", sErr.IndexName)
	assert.Nil(t, result)
}

// ---------------------------------------------------------------------------
// start.go: defaultMVCCFastStorageFactory inner closure (lines 215-228)
// These test the inner factory closure specifically (different from the outer
// function test in coverage_boost_test.go).
// ---------------------------------------------------------------------------

func TestDefaultMVCCFastStorageFactory_ClosureCreatesDifferentInstances(t *testing.T) {
	factory := defaultMVCCFastStorageFactory()
	require.NotNil(t, factory)

	// Two different shard/replica IDs must produce distinct storage instances.
	s1 := factory(1, 1)
	s2 := factory(2, 2)
	require.NotNil(t, s1)
	require.NotNil(t, s2)
	assert.NotSame(t, s1, s2)
}

// ---------------------------------------------------------------------------
// tls.go: generateCA (lines 118-163) - additional coverage
// ---------------------------------------------------------------------------

func TestGenerateCA_SerialNumberPositive(t *testing.T) {
	_, _, caCert, _, err := generateCA(time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 1, caCert.SerialNumber.Sign(),
		"generated CA serial number must be positive")
}

func TestGenerateCA_SubjectCommonName(t *testing.T) {
	_, _, caCert, _, err := generateCA(24 * time.Hour)
	require.NoError(t, err)
	assert.Equal(t, "qrdb-ca", caCert.Subject.CommonName)
}

// ---------------------------------------------------------------------------
// tls.go: generateNodeCert (lines 168-214) - additional coverage
// ---------------------------------------------------------------------------

func TestGenerateNodeCert_BothAuthUsages(t *testing.T) {
	_, _, caCert, caKey, err := generateCA(24 * time.Hour)
	require.NoError(t, err)

	certPEM, _, err := generateNodeCert(caCert, caKey, time.Hour)
	require.NoError(t, err)

	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block)
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	assert.Contains(t, leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth,
		"node cert must have ServerAuth for inbound Raft connections")
	assert.Contains(t, leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth,
		"node cert must have ClientAuth for outbound Raft connections")
}

// ---------------------------------------------------------------------------
// tls.go: generateClientCert (lines 220-262) - additional coverage
// ---------------------------------------------------------------------------

func TestGenerateClientCert_OnlyClientAuth(t *testing.T) {
	_, _, caCert, caKey, err := generateCA(24 * time.Hour)
	require.NoError(t, err)

	certPEM, _, err := generateClientCert(caCert, caKey, "svc", "tenant-x", time.Hour)
	require.NoError(t, err)

	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	assert.Contains(t, cert.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
	for _, ku := range cert.ExtKeyUsage {
		assert.NotEqual(t, x509.ExtKeyUsageServerAuth, ku,
			"client cert must not carry ServerAuth")
	}
}

// ---------------------------------------------------------------------------
// tls.go: generateLeafFromCA - additional missing path coverage
// ---------------------------------------------------------------------------

func TestGenerateLeafFromCA_InvalidCACertDERAdditional(t *testing.T) {
	// CERTIFICATE block present but with unparse-able DER.
	badCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbage-der-bytes")})
	keyPEM, _, _, _, err := generateCA(time.Hour)
	require.NoError(t, err)

	bundlePEM := append(badCertPEM, keyPEM...)
	bundle, err := generateLeafFromCA(bundlePEM)
	require.Error(t, err)
	var bke *BootstrapKeyError
	require.True(t, errors.As(err, &bke))
	assert.Equal(t, "CA certificate", bke.Kind)
	assert.Nil(t, bundle)
}

// ---------------------------------------------------------------------------
// bootstrap.go: resolveTLS path 2 with ca-key.pem present
// ---------------------------------------------------------------------------

func TestResolveTLS_Path2_CachedCertsWithCAKey(t *testing.T) {
	dataDir := t.TempDir()

	// Generate and persist a full bundle including the CA key, through the
	// storage primitive itself. cmd/qrdbd's persistFullBundle wrapper was
	// deleted with go-qrdb#264: it had no production caller and, writing
	// material without a role, could only ever produce a data directory the
	// daemon now refuses to boot on.
	bundle, err := generateCATLS()
	require.NoError(t, err)
	require.NoError(t, clustertls.PersistMaterial(&clustertls.Material{
		CACertPEM: bundle.CACertPEM,
		CAKeyPEM:  bundle.CAKeyPEM,
		CertPEM:   bundle.CertPEM,
		KeyPEM:    bundle.KeyPEM,
	}, dataDir))

	// Also write ca-key.pem so loadTLSFromDir picks it up.
	caKeyPath := filepath.Join(dataDir, "ca-key.pem")
	require.NoError(t, os.WriteFile(caKeyPath, bundle.CAKeyPEM, 0o600))

	// And the enrollment role, which every path that writes CA material
	// records. Without it this is the pre-role data directory go-qrdb#264
	// refuses to boot on rather than a cache.
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, clusterenroll.RoleFile),
		[]byte(clusterenroll.RoleSingleNode), 0o600))

	f := &startFlags{
		dataDir:         dataDir,
		discoveryMethod: "static",
		peers:           []string{},
		host:            testBindHost,
	}

	loaded, servers, err := resolveTLS(f, newDiscardLogger())
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Nil(t, servers)
	assert.NotEmpty(t, loaded.CAKeyPEM,
		"CAKeyPEM must be loaded when ca-key.pem is present")
}

// ---------------------------------------------------------------------------
// test_entities.go: bridgeTestUser marshal/unmarshal exercises init closures
// ---------------------------------------------------------------------------

func TestBridgeTestUser_JSONMarshalUnmarshal(t *testing.T) {
	original := &bridgeTestUser{
		ID:    42,
		Name:  "Alice",
		Email: "alice@example.com",
		Age:   30,
		Score: 9.5,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	restored := &bridgeTestUser{}
	require.NoError(t, json.Unmarshal(data, restored))

	assert.Equal(t, original.ID, restored.ID)
	assert.Equal(t, original.Name, restored.Name)
	assert.Equal(t, original.Email, restored.Email)
	assert.InDelta(t, original.Score, restored.Score, 1e-9)
}

func TestBridgeTestUser_JSONUnmarshal_InvalidJSON(t *testing.T) {
	target := &bridgeTestUser{}
	err := json.Unmarshal([]byte("not-json"), target)
	require.Error(t, err, "invalid JSON must return an error")
}

func TestBridgeTestUser_ZeroValue(t *testing.T) {
	// Zero-value bridgeTestUser must marshal and unmarshal cleanly.
	original := &bridgeTestUser{}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	restored := &bridgeTestUser{}
	require.NoError(t, json.Unmarshal(data, restored))
	assert.Equal(t, original, restored)
}

// ---------------------------------------------------------------------------
// test_entities.go: call NewFunc, MarshalFunc, and UnmarshalFunc closures
// registered in init() to cover those statements (currently at 25%).
// ---------------------------------------------------------------------------

func TestBridgeTestUser_RegistryNewFunc(t *testing.T) {
	reg, ok := entity.Global().Get("bridge_users")
	require.True(t, ok, "bridge_users must be registered")
	require.NotNil(t, reg.NewFunc)

	v := reg.NewFunc()
	require.NotNil(t, v)
	_, ok = v.(*bridgeTestUser)
	assert.True(t, ok, "NewFunc must return *bridgeTestUser")
}

func TestBridgeTestUser_RegistryMarshalFunc(t *testing.T) {
	reg, ok := entity.Global().Get("bridge_users")
	require.True(t, ok)
	require.NotNil(t, reg.MarshalFunc)

	u := &bridgeTestUser{ID: 1, Name: "Alice", Email: "a@b.com", Age: 25, Score: 9.1}
	data, err := reg.MarshalFunc(u)
	require.NoError(t, err)
	assert.NotEmpty(t, data)
}

func TestBridgeTestUser_RegistryUnmarshalFunc(t *testing.T) {
	reg, ok := entity.Global().Get("bridge_users")
	require.True(t, ok)
	require.NotNil(t, reg.UnmarshalFunc)

	data := []byte(`{"id":7,"name":"Bob","email":"b@c.com","age":30,"score":8.2}`)
	v, err := reg.UnmarshalFunc(data)
	require.NoError(t, err)
	u, ok := v.(*bridgeTestUser)
	require.True(t, ok)
	assert.Equal(t, uint64(7), u.ID)
	assert.Equal(t, "Bob", u.Name)
}

func TestBridgeTestUser_RegistryUnmarshalFunc_InvalidJSON(t *testing.T) {
	reg, ok := entity.Global().Get("bridge_users")
	require.True(t, ok)
	require.NotNil(t, reg.UnmarshalFunc)

	_, err := reg.UnmarshalFunc([]byte("not-json"))
	require.Error(t, err, "invalid JSON must return an error from UnmarshalFunc")
}

// noopLookup is a lookup that answers nothing. Its presence, not its answers,
// is what RegisterEntityIndexes requires: an adapter with a registerer but NO
// lookup can create indexes it could never read, so it refuses registration
// rather than reporting a capability it does not have.
func noopLookup(_ context.Context, _ interface{}) (interface{}, error) {
	return nil, nil
}

// TestKVServiceAdapter_RegisterEntityIndexes_RefusesCompound covers a review
// finding. QueryCompoundIndex and ScanCompoundIndex on this adapter refuse
// unconditionally — it exposes no compound operations at all — so forwarding a
// compound definition to the registerer and returning nil told the caller a
// compound index existed that it could never query. Registration success has
// to be an accurate capability statement or it is worse than no answer.
func TestKVServiceAdapter_RegisterEntityIndexes_RefusesCompound(t *testing.T) {
	var forwarded int
	registerer := &mockEntityRegisterer{
		registerFn: func(_ string, _ []kvstore.EntityIndex) error {
			forwarded++
			return nil
		},
	}
	adapter := newKVServiceAdapter(&mockKVServicer{}, registerer, noopLookup)

	err := adapter.RegisterEntityIndexes("member", []kvstore.EntityIndex{{
		FieldNames:     []string{"Team", "Name"},
		JSONFieldNames: []string{"team", "name"},
	}})
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("RegisterEntityIndexes = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if forwarded != 0 {
		t.Error("a refused definition must not reach the registerer")
	}
}

// TestKVServiceAdapter_RegisterEntityIndexes_RefusesWithoutLookup covers the
// other half: a registerer with NO lookup can create indexes it can never read,
// because both query methods refuse when lookup is nil. Reporting successful
// registration there is a false capability statement.
func TestKVServiceAdapter_RegisterEntityIndexes_RefusesWithoutLookup(t *testing.T) {
	var forwarded int
	registerer := &mockEntityRegisterer{
		registerFn: func(_ string, _ []kvstore.EntityIndex) error {
			forwarded++
			return nil
		},
	}
	adapter := newKVServiceAdapter(&mockKVServicer{}, registerer, nil)

	err := adapter.RegisterEntityIndexes("member", []kvstore.EntityIndex{
		{FieldName: "Team", JSONFieldName: "team"},
	})
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("RegisterEntityIndexes = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if forwarded != 0 {
		t.Error("a refused registration must not reach the registerer")
	}
}
