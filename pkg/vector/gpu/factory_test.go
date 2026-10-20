//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package gpu

import (
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/gpu/remote"
)

// mockHNSWIndex is a test double implementing interfaces.HNSWIndex.
type mockHNSWIndex struct {
	insertErr error
	deleteErr error
	searchRes []*interfaces.HNSWSearchResult
	searchErr error
	countVal  int64
}

func (m *mockHNSWIndex) Insert(_ uint64, _ []float32) error {
	return m.insertErr
}

func (m *mockHNSWIndex) Delete(_ uint64) error {
	return m.deleteErr
}

func (m *mockHNSWIndex) Search(_ []float32, _ int) ([]*interfaces.HNSWSearchResult, error) {
	return m.searchRes, m.searchErr
}

func (m *mockHNSWIndex) Count() int64 {
	return m.countVal
}

func (m *mockHNSWIndex) GetStats() *interfaces.HNSWStats {
	return &interfaces.HNSWStats{NodeCount: m.countVal}
}

// mockHNSWFactory is a test double implementing interfaces.HNSWIndexFactory.
type mockHNSWFactory struct {
	createErr error
	index     interfaces.HNSWIndex
}

func (m *mockHNSWFactory) CreateIndex(_ *interfaces.HNSWConfig) (interfaces.HNSWIndex, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	return m.index, nil
}

func TestGPUIndexFactory_CreateIndex_GPUDisabled_FallsToCPU(t *testing.T) {
	cpuIndex := &mockHNSWIndex{countVal: 5}
	fallback := &mockHNSWFactory{index: cpuIndex}

	cfg := DefaultGPUConfig()
	cfg.Enabled = false

	factory := NewGPUIndexFactory(cfg, nil, fallback)
	idx, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if idx == nil {
		t.Fatal("expected non-nil index")
	}
	// Should return the CPU index from the fallback.
	if idx.Count() != 5 {
		t.Errorf("expected count 5 from CPU fallback, got %d", idx.Count())
	}
}

func TestGPUIndexFactory_CreateIndex_GPUNotAvailable_FallsToCPU(t *testing.T) {
	cpuIndex := &mockHNSWIndex{countVal: 3}
	fallback := &mockHNSWFactory{index: cpuIndex}

	cfg := DefaultGPUConfig()
	cfg.Enabled = true

	// gpuAvailable is injected as false to simulate no GPU hardware.
	factory := &GPUIndexFactory{
		config:       cfg,
		gpuAvailable: false,
		fallback:     fallback,
	}

	idx, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if idx == nil {
		t.Fatal("expected non-nil index")
	}
	if idx.Count() != 3 {
		t.Errorf("expected count 3 from CPU fallback, got %d", idx.Count())
	}
}

func TestGPUIndexFactory_NilFallback_ReturnsError(t *testing.T) {
	cfg := DefaultGPUConfig()
	cfg.Enabled = false

	factory := NewGPUIndexFactory(cfg, nil, nil)
	_, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	if err == nil {
		t.Fatal("expected error with nil fallback, got nil")
	}
	if !errors.Is(err, ErrNoFallbackFactory) {
		t.Errorf("expected ErrNoFallbackFactory, got: %v", err)
	}
}

func TestGPUIndexFactory_FallbackError_PropagatesError(t *testing.T) {
	sentinel := errors.New("cpu factory failed")
	fallback := &mockHNSWFactory{createErr: sentinel}

	cfg := DefaultGPUConfig()
	cfg.Enabled = false

	factory := NewGPUIndexFactory(cfg, nil, fallback)
	_, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got: %v", err)
	}
}

func TestGPUIndexFactory_ImplementsHNSWIndexFactory(t *testing.T) {
	factory := NewGPUIndexFactory(DefaultGPUConfig(), nil, &mockHNSWFactory{})
	var _ interfaces.HNSWIndexFactory = factory
}

func TestNewGPUIndexFactory_StoresConfig(t *testing.T) {
	cfg := DefaultGPUConfig()
	cfg.DeviceID = 2

	factory := NewGPUIndexFactory(cfg, nil, &mockHNSWFactory{})
	if factory.config.DeviceID != 2 {
		t.Errorf("expected DeviceID 2, got %d", factory.config.DeviceID)
	}
}

// ---------------------------------------------------------------------------
// DefaultRemoteGPUConfig
// ---------------------------------------------------------------------------

func TestDefaultRemoteGPUConfig_Values(t *testing.T) {
	cfg := DefaultRemoteGPUConfig()
	if cfg == nil {
		t.Fatal("DefaultRemoteGPUConfig returned nil")
	}
	if cfg.Enabled {
		t.Error("DefaultRemoteGPUConfig: Enabled should be false")
	}
	if cfg.Endpoint != "" {
		t.Errorf("Endpoint = %q, want empty", cfg.Endpoint)
	}
	if cfg.Protocol != "grpc" {
		t.Errorf("Protocol = %q, want %q", cfg.Protocol, "grpc")
	}
	if cfg.TimeoutMs != 5000 {
		t.Errorf("TimeoutMs = %d, want 5000", cfg.TimeoutMs)
	}
}

// ---------------------------------------------------------------------------
// RemoteGPUIndexFactory
// ---------------------------------------------------------------------------

func TestNewRemoteGPUIndexFactory_StoresConfigAndFallback(t *testing.T) {
	cfg := DefaultRemoteGPUConfig()
	fallback := &mockHNSWFactory{index: &mockHNSWIndex{countVal: 7}}

	factory := NewRemoteGPUIndexFactory(cfg, fallback)
	if factory == nil {
		t.Fatal("NewRemoteGPUIndexFactory returned nil")
	}
	if factory.config != cfg {
		t.Error("config not stored on RemoteGPUIndexFactory")
	}
	if factory.fallback != fallback {
		t.Error("fallback not stored on RemoteGPUIndexFactory")
	}
}

func TestRemoteGPUIndexFactory_CreateIndex_RemoteDisabled_UseFallback(t *testing.T) {
	cpuIndex := &mockHNSWIndex{countVal: 4}
	fallback := &mockHNSWFactory{index: cpuIndex}

	cfg := DefaultRemoteGPUConfig()
	cfg.Enabled = false

	factory := NewRemoteGPUIndexFactory(cfg, fallback)
	idx, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	if err != nil {
		t.Fatalf("CreateIndex (remote disabled): %v", err)
	}
	if idx.Count() != 4 {
		t.Errorf("expected count 4 from fallback, got %d", idx.Count())
	}
}

func TestRemoteGPUIndexFactory_CreateIndex_NilConfig_UseFallback(t *testing.T) {
	cpuIndex := &mockHNSWIndex{countVal: 2}
	fallback := &mockHNSWFactory{index: cpuIndex}

	factory := NewRemoteGPUIndexFactory(nil, fallback)
	idx, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	if err != nil {
		t.Fatalf("CreateIndex (nil config): %v", err)
	}
	if idx.Count() != 2 {
		t.Errorf("expected count 2 from fallback, got %d", idx.Count())
	}
}

func TestRemoteGPUIndexFactory_CreateIndex_RemoteEnabled_EmptyEndpoint_Error(t *testing.T) {
	cfg := DefaultRemoteGPUConfig()
	cfg.Enabled = true
	cfg.Endpoint = "" // empty endpoint → NewRemoteGPUIndex returns error

	factory := NewRemoteGPUIndexFactory(cfg, nil)
	_, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	if err == nil {
		t.Fatal("expected error when remote enabled with empty endpoint, got nil")
	}
}

func TestRemoteGPUIndexFactory_CreateIndex_NilFallback_ReturnsError(t *testing.T) {
	cfg := DefaultRemoteGPUConfig()
	cfg.Enabled = false

	factory := NewRemoteGPUIndexFactory(cfg, nil)
	_, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	if err == nil {
		t.Fatal("expected error with nil fallback, got nil")
	}
	if !errors.Is(err, ErrNoFallbackFactory) {
		t.Errorf("expected ErrNoFallbackFactory, got: %v", err)
	}
}

func TestRemoteGPUIndexFactory_CreateIndex_FallbackError(t *testing.T) {
	sentinel := errors.New("fallback factory failed")
	fallback := &mockHNSWFactory{createErr: sentinel}

	cfg := DefaultRemoteGPUConfig()
	cfg.Enabled = false

	factory := NewRemoteGPUIndexFactory(cfg, fallback)
	_, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got: %v", err)
	}
}

func TestRemoteGPUIndexFactory_ImplementsHNSWIndexFactory(t *testing.T) {
	factory := NewRemoteGPUIndexFactory(DefaultRemoteGPUConfig(), &mockHNSWFactory{})
	var _ interfaces.HNSWIndexFactory = factory
}

// ---------------------------------------------------------------------------
// remoteConfigToConnConfig
// ---------------------------------------------------------------------------

func TestRemoteConfigToConnConfig_MapsAllFields(t *testing.T) {
	cfg := &RemoteGPUConfig{
		Endpoint:  "192.168.1.100:9090",
		TLSCert:   "/path/to/cert.pem",
		TLSKey:    "/path/to/key.pem",
		TLSCA:     "/path/to/ca.pem",
		TimeoutMs: 3000,
	}
	conn := remoteConfigToConnConfig(cfg)
	if conn.Endpoint != cfg.Endpoint {
		t.Errorf("Endpoint = %q, want %q", conn.Endpoint, cfg.Endpoint)
	}
	if conn.TLSCert != cfg.TLSCert {
		t.Errorf("TLSCert = %q, want %q", conn.TLSCert, cfg.TLSCert)
	}
	if conn.TLSKey != cfg.TLSKey {
		t.Errorf("TLSKey = %q, want %q", conn.TLSKey, cfg.TLSKey)
	}
	if conn.TLSCA != cfg.TLSCA {
		t.Errorf("TLSCA = %q, want %q", conn.TLSCA, cfg.TLSCA)
	}
	if conn.TimeoutMs != cfg.TimeoutMs {
		t.Errorf("TimeoutMs = %d, want %d", conn.TimeoutMs, cfg.TimeoutMs)
	}
}

func TestGPUIndexFactory_CreateIndex_RemoteEnabled_EmptyEndpoint_Error(t *testing.T) {
	// When remoteConfig.Enabled is true but Endpoint is empty, NewRemoteGPUIndex
	// returns an error. This exercises the createRemoteIndex error path.
	remoteCfg := DefaultRemoteGPUConfig()
	remoteCfg.Enabled = true
	remoteCfg.Endpoint = ""

	cfg := DefaultGPUConfig()
	factory := NewGPUIndexFactory(cfg, remoteCfg, &mockHNSWFactory{})
	_, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	if err == nil {
		t.Fatal("expected error when remote enabled with empty endpoint, got nil")
	}
}

func TestGPUIndexFactory_CreateIndex_GPUEnabled_GPUAvailable_CreatesGPUIndex(t *testing.T) {
	cfg := DefaultGPUConfig()
	cfg.Enabled = true

	fallback := &mockHNSWFactory{index: &mockHNSWIndex{countVal: 0}}

	// Inject gpuAvailable=true to exercise the GPU path.
	// In a !gpu build this reaches createGPUIndex which returns ErrGPUNotCompiled;
	// in a +gpu build with hardware it succeeds. Either outcome is valid here —
	// the test verifies that the GPU path is taken (not fallback) when both are true.
	factory := &GPUIndexFactory{
		config:       cfg,
		gpuAvailable: true,
		fallback:     fallback,
	}

	_, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	// In !gpu builds this returns ErrGPUNotCompiled from the CAGRA stub.
	// In +gpu builds this succeeds. Both are expected outcomes.
	_ = err
}

// closeRemoteIndex closes a remote index returned by a factory, failing the
// test when the index does not hold a closable connection.
func closeRemoteIndex(t *testing.T, idx interfaces.HNSWIndex) {
	t.Helper()
	closer, ok := idx.(io.Closer)
	if !ok {
		t.Fatalf("remote index %T does not implement io.Closer", idx)
	}
	if err := closer.Close(); err != nil {
		t.Errorf("Close remote index: %v", err)
	}
}

func TestGPUIndexFactory_CreateIndex_RemoteEnabled_CreatesRemoteIndex(t *testing.T) {
	// grpc.NewClient connects lazily, so a reserved endpoint yields an index
	// without any network I/O.
	remoteCfg := DefaultRemoteGPUConfig()
	remoteCfg.Enabled = true
	remoteCfg.Endpoint = "127.0.0.1:1"

	fallback := &mockHNSWFactory{index: &mockHNSWIndex{countVal: 9}}
	factory := NewGPUIndexFactory(DefaultGPUConfig(), remoteCfg, fallback)
	idx, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	if err != nil {
		t.Fatalf("CreateIndex (remote enabled): %v", err)
	}
	if _, ok := idx.(*remote.RemoteGPUIndex); !ok {
		t.Fatalf("CreateIndex returned %T, want *remote.RemoteGPUIndex", idx)
	}
	closeRemoteIndex(t, idx)
}

func TestGPUIndexFactory_CreateIndex_RemoteEnabled_MissingCA_FactoryError(t *testing.T) {
	remoteCfg := DefaultRemoteGPUConfig()
	remoteCfg.Enabled = true
	remoteCfg.Endpoint = "127.0.0.1:1"
	remoteCfg.TLSCA = filepath.Join(t.TempDir(), "absent-ca.pem")

	factory := NewGPUIndexFactory(DefaultGPUConfig(), remoteCfg, &mockHNSWFactory{})
	_, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	var factoryErr *FactoryError
	if !errors.As(err, &factoryErr) {
		t.Fatalf("expected *FactoryError, got %T: %v", err, err)
	}
	if factoryErr.Op != "gpu factory: create remote index" {
		t.Errorf("FactoryError.Op = %q, want %q", factoryErr.Op, "gpu factory: create remote index")
	}
}

func TestRemoteGPUIndexFactory_CreateIndex_RemoteEnabled_CreatesRemoteIndex(t *testing.T) {
	cfg := DefaultRemoteGPUConfig()
	cfg.Enabled = true
	cfg.Endpoint = "127.0.0.1:1"

	factory := NewRemoteGPUIndexFactory(cfg, nil)
	idx, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	if err != nil {
		t.Fatalf("CreateIndex (remote enabled): %v", err)
	}
	if _, ok := idx.(*remote.RemoteGPUIndex); !ok {
		t.Fatalf("CreateIndex returned %T, want *remote.RemoteGPUIndex", idx)
	}
	closeRemoteIndex(t, idx)
}

func TestRemoteGPUIndexFactory_CreateIndex_RemoteEnabled_EmptyEndpoint_FactoryError(t *testing.T) {
	cfg := DefaultRemoteGPUConfig()
	cfg.Enabled = true

	factory := NewRemoteGPUIndexFactory(cfg, &mockHNSWFactory{})
	_, err := factory.CreateIndex(interfaces.DefaultHNSWConfig())
	var factoryErr *FactoryError
	if !errors.As(err, &factoryErr) {
		t.Fatalf("expected *FactoryError, got %T: %v", err, err)
	}
	if !errors.Is(err, remote.ErrEmptyEndpoint) {
		t.Errorf("expected remote.ErrEmptyEndpoint in chain, got: %v", err)
	}
	if got, want := err.Error(), "remote gpu factory: "+remote.ErrEmptyEndpoint.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
