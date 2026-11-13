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

package batch

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// mockProposer is a minimal test double implementing the Proposer interface.
type mockProposer struct {
	err error
}

func (m *mockProposer) SyncPropose(_ context.Context, _ uint64, _ []byte) error {
	return m.err
}

// newTestRegistry returns an isolated Prometheus registry for test isolation.
func newTestRegistry(t *testing.T) *prometheus.Registry {
	t.Helper()
	return prometheus.NewRegistry()
}

// ---------------------------------------------------------------------------
// DefaultConfig
// ---------------------------------------------------------------------------

func Test_DefaultConfig_returnsCorrectDefaults(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()

	if cfg.MaxBatchSize != 1000 {
		t.Errorf("MaxBatchSize = %d, want 1000", cfg.MaxBatchSize)
	}
	if cfg.FlushInterval != 50*time.Millisecond {
		t.Errorf("FlushInterval = %v, want 50ms", cfg.FlushInterval)
	}
	if cfg.QueueSize != 10000 {
		t.Errorf("QueueSize = %d, want 10000", cfg.QueueSize)
	}
}

func Test_DefaultConfig_matchesReExportedConstants(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()

	if cfg.MaxBatchSize != DefaultMaxBatchSize {
		t.Errorf("MaxBatchSize = %d, want DefaultMaxBatchSize %d",
			cfg.MaxBatchSize, DefaultMaxBatchSize)
	}
	if cfg.FlushInterval != DefaultFlushInterval {
		t.Errorf("FlushInterval = %v, want DefaultFlushInterval %v",
			cfg.FlushInterval, DefaultFlushInterval)
	}
	if cfg.QueueSize != DefaultQueueSize {
		t.Errorf("QueueSize = %d, want DefaultQueueSize %d",
			cfg.QueueSize, DefaultQueueSize)
	}
}

// ---------------------------------------------------------------------------
// EncodeBatch / DecodeBatch round-trip
// ---------------------------------------------------------------------------

func Test_EncodeBatch_singleCommand(t *testing.T) {
	t.Parallel()
	cmd := []byte("PUT key1 value1")
	encoded, err := EncodeBatch([][]byte{cmd})
	if err != nil {
		t.Fatalf("EncodeBatch: %v", err)
	}

	// 4-byte length prefix + len(cmd) data bytes.
	expectedLen := 4 + len(cmd)
	if len(encoded) != expectedLen {
		t.Fatalf("encoded length = %d, want %d", len(encoded), expectedLen)
	}

	decoded, err := DecodeBatch(encoded)
	if err != nil {
		t.Fatalf("DecodeBatch failed: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("decoded %d commands, want 1", len(decoded))
	}
	if !bytes.Equal(decoded[0], cmd) {
		t.Errorf("decoded[0] = %q, want %q", decoded[0], cmd)
	}
}

func Test_EncodeBatch_nilSlice(t *testing.T) {
	t.Parallel()
	result, err := EncodeBatch(nil)
	if err != nil {
		t.Fatalf("EncodeBatch(nil): %v", err)
	}
	if len(result) != 0 {
		t.Errorf("EncodeBatch(nil) produced %d bytes, want 0", len(result))
	}
}

func Test_DecodeBatch_nilInput(t *testing.T) {
	t.Parallel()
	result, err := DecodeBatch(nil)
	if err != nil {
		t.Fatalf("DecodeBatch(nil) returned error: %v", err)
	}
	if result != nil {
		t.Errorf("DecodeBatch(nil) = %v, want nil", result)
	}
}

func Test_DecodeBatch_truncatedPrefix(t *testing.T) {
	t.Parallel()
	// Only 2 bytes when 4 are needed for a length prefix.
	data := []byte{0x00, 0x01}
	_, err := DecodeBatch(data)
	if err == nil {
		t.Fatal("DecodeBatch with truncated prefix should return error")
	}

	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("error should be *DecodeError, got %T", err)
	}
	if !errors.Is(err, ErrDecode) {
		t.Error("error should match ErrDecode sentinel")
	}
}

func Test_DecodeBatch_truncatedData(t *testing.T) {
	t.Parallel()
	// Length prefix says 10 bytes, but only 3 bytes of data follow.
	data := []byte{
		0x00, 0x00, 0x00, 0x0A, // length = 10
		0x01, 0x02, 0x03, // only 3 bytes of data
	}
	_, err := DecodeBatch(data)
	if err == nil {
		t.Fatal("DecodeBatch with truncated data should return error")
	}

	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("error should be *DecodeError, got %T", err)
	}
	if !errors.Is(err, ErrDecode) {
		t.Error("error should match ErrDecode sentinel")
	}
}

func Test_DecodeBatch_garbageMaxLength(t *testing.T) {
	t.Parallel()
	// A valid prefix claiming a huge length that exceeds the payload.
	data := []byte{0xFF, 0xFF, 0xFF, 0xFF}
	_, err := DecodeBatch(data)
	if err == nil {
		t.Fatal("DecodeBatch with garbage data should return error")
	}
	if !errors.Is(err, ErrDecode) {
		t.Error("error should match ErrDecode sentinel")
	}
}

func Test_DecodeBatch_truncatedSecondPrefix(t *testing.T) {
	t.Parallel()
	// First command valid (length=1, data=0xFF), then 2 bytes of garbage.
	data := []byte{
		0x00, 0x00, 0x00, 0x01, // length = 1
		0xFF,       // 1 byte of data
		0x00, 0x01, // truncated second prefix
	}
	_, err := DecodeBatch(data)
	if err == nil {
		t.Fatal("DecodeBatch with truncated second prefix should return error")
	}
	if !errors.Is(err, ErrDecode) {
		t.Error("error should match ErrDecode sentinel")
	}
}

func Test_RoundTrip_mixedSizes(t *testing.T) {
	t.Parallel()
	original := [][]byte{
		[]byte("short"),
		make([]byte, 4096),
		{},
		[]byte("binary-\x00\x01\x02\xFF"),
	}
	for i := range original[1] {
		original[1][i] = byte(i % 256)
	}
	encoded, encErr2 := EncodeBatch(original)
	if encErr2 != nil {
		t.Fatalf("EncodeBatch: %v", encErr2)
	}
	decoded, err := DecodeBatch(encoded)
	if err != nil {
		t.Fatalf("round-trip decode failed: %v", err)
	}
	if len(decoded) != len(original) {
		t.Fatalf("decoded %d commands, want %d", len(decoded), len(original))
	}
	for i := range original {
		if !bytes.Equal(decoded[i], original[i]) {
			t.Errorf("decoded[%d] mismatch", i)
		}
	}
}

// ---------------------------------------------------------------------------
// NewAggregator
// ---------------------------------------------------------------------------

func Test_NewAggregator_nilProposerReturnsNilProposerError(t *testing.T) {
	t.Parallel()
	agg, err := NewAggregator(nil)
	if err == nil {
		t.Fatal("NewAggregator(nil) should return error")
	}
	if agg != nil {
		t.Error("aggregator should be nil when proposer is nil")
	}

	var npe *NilProposerError
	if !errors.As(err, &npe) {
		t.Fatalf("error should be *NilProposerError, got %T", err)
	}
	if !errors.Is(err, ErrNilProposer) {
		t.Error("error should match ErrNilProposer sentinel")
	}
}

func Test_NewAggregator_defaultsProduceWorkingAggregator(t *testing.T) {
	t.Parallel()
	mp := &mockProposer{}
	// NewAggregator with no WithConfig uses DefaultConfig. Verify it works
	// by successfully submitting and closing.
	agg, err := NewAggregator(mp, WithRegistry(newTestRegistry(t)))
	if err != nil {
		t.Fatalf("NewAggregator failed: %v", err)
	}
	defer func() { _ = agg.Close() }()

	submitErr := agg.Submit(context.Background(), 1, []byte("default-cfg-test"))
	if submitErr != nil {
		t.Errorf("Submit with default config failed: %v", submitErr)
	}
}

func Test_NewAggregator_closeIdempotent(t *testing.T) {
	t.Parallel()
	mp := &mockProposer{}
	agg, err := NewAggregator(mp, WithRegistry(newTestRegistry(t)))
	if err != nil {
		t.Fatalf("NewAggregator failed: %v", err)
	}

	for i := range 5 {
		if closeErr := agg.Close(); closeErr != nil {
			t.Errorf("Close() call %d returned error: %v", i, closeErr)
		}
	}
}

// ---------------------------------------------------------------------------
// Submit on closed aggregator
// ---------------------------------------------------------------------------

func Test_Submit_afterCloseReturnsClosedError(t *testing.T) {
	t.Parallel()
	mp := &mockProposer{}
	agg, err := NewAggregator(mp,
		WithConfig(Config{
			MaxBatchSize:  10,
			FlushInterval: 5 * time.Millisecond,
			QueueSize:     100,
		}),
		WithRegistry(newTestRegistry(t)),
	)
	if err != nil {
		t.Fatalf("NewAggregator failed: %v", err)
	}
	if closeErr := agg.Close(); closeErr != nil {
		t.Fatalf("Close failed: %v", closeErr)
	}

	submitErr := agg.Submit(context.Background(), 1, []byte("data"))
	if submitErr == nil {
		t.Fatal("Submit after Close should return error")
	}

	var ce *ClosedError
	if !errors.As(submitErr, &ce) {
		t.Fatalf("error should be *ClosedError, got %T: %v", submitErr, submitErr)
	}
	if !errors.Is(submitErr, ErrClosed) {
		t.Error("error should match ErrClosed sentinel")
	}
}

// ---------------------------------------------------------------------------
// WithConfig
// ---------------------------------------------------------------------------

func Test_WithConfig_customValuesAccepted(t *testing.T) {
	t.Parallel()
	mp := &mockProposer{}
	cfg := Config{
		MaxBatchSize:  50,
		FlushInterval: 200 * time.Millisecond,
		QueueSize:     500,
	}
	agg, err := NewAggregator(mp,
		WithConfig(cfg),
		WithRegistry(newTestRegistry(t)),
	)
	if err != nil {
		t.Fatalf("NewAggregator with custom config failed: %v", err)
	}
	defer func() { _ = agg.Close() }()

	// Verify the aggregator is functional with the custom config.
	submitErr := agg.Submit(context.Background(), 1, []byte("custom-cfg"))
	if submitErr != nil {
		t.Errorf("Submit with custom config failed: %v", submitErr)
	}
}

func Test_WithConfig_zeroFieldsDoNotPanic(t *testing.T) {
	t.Parallel()
	mp := &mockProposer{}
	// Passing an empty Config should not panic; zero fields are replaced
	// with defaults by the upstream implementation.
	agg, err := NewAggregator(mp,
		WithConfig(Config{}),
		WithRegistry(newTestRegistry(t)),
	)
	if err != nil {
		t.Fatalf("NewAggregator with zero config failed: %v", err)
	}
	defer func() { _ = agg.Close() }()

	submitErr := agg.Submit(context.Background(), 1, []byte("zero-cfg"))
	if submitErr != nil {
		t.Errorf("Submit with zero config failed: %v", submitErr)
	}
}

// ---------------------------------------------------------------------------
// WithLogger
// ---------------------------------------------------------------------------

func Test_WithLogger_customLoggerAccepted(t *testing.T) {
	t.Parallel()
	mp := &mockProposer{}
	logger := slog.New(slog.NewTextHandler(nil, nil))
	// WithLogger should not cause an error or panic.
	agg, err := NewAggregator(mp,
		WithLogger(logger),
		WithRegistry(newTestRegistry(t)),
	)
	if err != nil {
		t.Fatalf("NewAggregator with custom logger failed: %v", err)
	}
	defer func() { _ = agg.Close() }()

	submitErr := agg.Submit(context.Background(), 1, []byte("logger-test"))
	if submitErr != nil {
		t.Errorf("Submit with custom logger failed: %v", submitErr)
	}
}

func Test_WithLogger_nilDoesNotPanic(t *testing.T) {
	t.Parallel()
	mp := &mockProposer{}
	// Passing nil logger should gracefully fall back to default.
	agg, err := NewAggregator(mp,
		WithLogger(nil),
		WithRegistry(newTestRegistry(t)),
	)
	if err != nil {
		t.Fatalf("NewAggregator with nil logger failed: %v", err)
	}
	defer func() { _ = agg.Close() }()

	submitErr := agg.Submit(context.Background(), 1, []byte("nil-logger-test"))
	if submitErr != nil {
		t.Errorf("Submit with nil logger failed: %v", submitErr)
	}
}

// ---------------------------------------------------------------------------
// WithRegistry
// ---------------------------------------------------------------------------

func Test_WithRegistry_nilFallsBackToDefault(t *testing.T) {
	t.Parallel()
	mp := &mockProposer{}
	// Passing nil then a real registry should work without panic.
	agg, err := NewAggregator(mp,
		WithRegistry(nil),
		WithRegistry(newTestRegistry(t)),
	)
	if err != nil {
		t.Fatalf("NewAggregator with nil+real registry failed: %v", err)
	}
	defer func() { _ = agg.Close() }()

	submitErr := agg.Submit(context.Background(), 1, []byte("nil-registry-test"))
	if submitErr != nil {
		t.Errorf("Submit with nil+real registry failed: %v", submitErr)
	}
}

// ---------------------------------------------------------------------------
// Sentinel errors via errors.Is
// ---------------------------------------------------------------------------

func Test_SentinelErrors_areDistinct(t *testing.T) {
	t.Parallel()
	sentinels := []error{ErrClosed, ErrQueueFull, ErrNilProposer, ErrFlush, ErrDecode}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i != j && errors.Is(a, b) {
				t.Errorf("sentinel %q should not match %q", a, b)
			}
		}
	}
}
