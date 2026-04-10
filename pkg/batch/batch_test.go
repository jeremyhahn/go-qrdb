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

func Test_DefaultConfig_allFieldsPositive(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()

	if cfg.MaxBatchSize <= 0 {
		t.Errorf("MaxBatchSize = %d, should be positive", cfg.MaxBatchSize)
	}
	if cfg.FlushInterval <= 0 {
		t.Errorf("FlushInterval = %v, should be positive", cfg.FlushInterval)
	}
	if cfg.QueueSize <= 0 {
		t.Errorf("QueueSize = %d, should be positive", cfg.QueueSize)
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
// Constants
// ---------------------------------------------------------------------------

func Test_DefaultMaxBatchSize_value(t *testing.T) {
	t.Parallel()
	if DefaultMaxBatchSize != 1000 {
		t.Errorf("DefaultMaxBatchSize = %d, want 1000", DefaultMaxBatchSize)
	}
}

func Test_DefaultFlushInterval_value(t *testing.T) {
	t.Parallel()
	if DefaultFlushInterval != 50*time.Millisecond {
		t.Errorf("DefaultFlushInterval = %v, want 50ms", DefaultFlushInterval)
	}
}

func Test_DefaultQueueSize_value(t *testing.T) {
	t.Parallel()
	if DefaultQueueSize != 10000 {
		t.Errorf("DefaultQueueSize = %d, want 10000", DefaultQueueSize)
	}
}

func Test_Constants_allPositive(t *testing.T) {
	t.Parallel()

	if DefaultMaxBatchSize <= 0 {
		t.Errorf("DefaultMaxBatchSize = %d, must be positive", DefaultMaxBatchSize)
	}
	if DefaultFlushInterval <= 0 {
		t.Errorf("DefaultFlushInterval = %v, must be positive", DefaultFlushInterval)
	}
	if DefaultQueueSize <= 0 {
		t.Errorf("DefaultQueueSize = %d, must be positive", DefaultQueueSize)
	}
}

// ---------------------------------------------------------------------------
// EncodeBatch / DecodeBatch round-trip
// ---------------------------------------------------------------------------

func Test_EncodeBatch_singleCommand(t *testing.T) {
	t.Parallel()
	cmd := []byte("PUT key1 value1")
	encoded := EncodeBatch([][]byte{cmd})

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

func Test_EncodeBatch_multipleCommands(t *testing.T) {
	t.Parallel()
	cmds := [][]byte{
		[]byte("GET key1"),
		[]byte("PUT key2 val2"),
		[]byte("DELETE key3"),
	}
	encoded := EncodeBatch(cmds)

	decoded, err := DecodeBatch(encoded)
	if err != nil {
		t.Fatalf("DecodeBatch failed: %v", err)
	}
	if len(decoded) != len(cmds) {
		t.Fatalf("decoded %d commands, want %d", len(decoded), len(cmds))
	}
	for i := range cmds {
		if !bytes.Equal(decoded[i], cmds[i]) {
			t.Errorf("decoded[%d] = %q, want %q", i, decoded[i], cmds[i])
		}
	}
}

func Test_EncodeBatch_nilSlice(t *testing.T) {
	t.Parallel()
	result := EncodeBatch(nil)
	if len(result) != 0 {
		t.Errorf("EncodeBatch(nil) produced %d bytes, want 0", len(result))
	}
}

func Test_EncodeBatch_emptySlice(t *testing.T) {
	t.Parallel()
	result := EncodeBatch([][]byte{})
	if len(result) != 0 {
		t.Errorf("EncodeBatch(empty) produced %d bytes, want 0", len(result))
	}
}

func Test_EncodeBatch_emptyCommands(t *testing.T) {
	t.Parallel()
	cmds := [][]byte{{}, {}, {}}
	encoded := EncodeBatch(cmds)

	decoded, err := DecodeBatch(encoded)
	if err != nil {
		t.Fatalf("DecodeBatch failed: %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("decoded %d commands, want 3", len(decoded))
	}
	for i, d := range decoded {
		if len(d) != 0 {
			t.Errorf("decoded[%d] has length %d, want 0", i, len(d))
		}
	}
}

func Test_EncodeBatch_binaryData(t *testing.T) {
	t.Parallel()
	cmd := make([]byte, 256)
	for i := range cmd {
		cmd[i] = byte(i)
	}
	encoded := EncodeBatch([][]byte{cmd})
	decoded, err := DecodeBatch(encoded)
	if err != nil {
		t.Fatalf("DecodeBatch failed: %v", err)
	}
	if !bytes.Equal(decoded[0], cmd) {
		t.Error("binary data round-trip failed")
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

func Test_DecodeBatch_emptyInput(t *testing.T) {
	t.Parallel()
	result, err := DecodeBatch([]byte{})
	if err != nil {
		t.Fatalf("DecodeBatch(empty) returned error: %v", err)
	}
	if result != nil {
		t.Errorf("DecodeBatch(empty) = %v, want nil", result)
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

func Test_RoundTrip_manyCommands(t *testing.T) {
	t.Parallel()
	const count = 500
	original := make([][]byte, count)
	for i := range original {
		original[i] = []byte{byte(i & 0xFF), byte((i >> 8) & 0xFF)}
	}
	encoded := EncodeBatch(original)
	decoded, err := DecodeBatch(encoded)
	if err != nil {
		t.Fatalf("round-trip decode failed: %v", err)
	}
	if len(decoded) != count {
		t.Fatalf("decoded %d commands, want %d", len(decoded), count)
	}
	for i := range original {
		if !bytes.Equal(decoded[i], original[i]) {
			t.Errorf("decoded[%d] mismatch", i)
		}
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
	encoded := EncodeBatch(original)
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

func Test_NewAggregator_nilProposerWithOptions(t *testing.T) {
	t.Parallel()
	_, err := NewAggregator(nil, WithConfig(Config{MaxBatchSize: 10}))
	if err == nil {
		t.Fatal("NewAggregator(nil, opts...) should return error")
	}
	if !errors.Is(err, ErrNilProposer) {
		t.Errorf("expected ErrNilProposer, got %v", err)
	}
}

func Test_NewAggregator_validProposerSucceeds(t *testing.T) {
	t.Parallel()
	mp := &mockProposer{}
	agg, err := NewAggregator(mp, WithRegistry(newTestRegistry(t)))
	if err != nil {
		t.Fatalf("NewAggregator failed: %v", err)
	}
	if agg == nil {
		t.Fatal("aggregator should not be nil")
	}
	if closeErr := agg.Close(); closeErr != nil {
		t.Errorf("Close failed: %v", closeErr)
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

func Test_Submit_afterCloseMatchesSentinel(t *testing.T) {
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
	_ = agg.Close()

	submitErr := agg.Submit(context.Background(), 1, []byte("data"))
	if !errors.Is(submitErr, ErrClosed) {
		t.Errorf("Submit after Close should return ErrClosed, got %v", submitErr)
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

func Test_WithRegistry_customRegistryAccepted(t *testing.T) {
	t.Parallel()
	mp := &mockProposer{}
	reg := prometheus.NewRegistry()
	// WithRegistry with a custom registry should not error or panic.
	agg, err := NewAggregator(mp, WithRegistry(reg))
	if err != nil {
		t.Fatalf("NewAggregator with custom registry failed: %v", err)
	}
	defer func() { _ = agg.Close() }()

	submitErr := agg.Submit(context.Background(), 1, []byte("registry-test"))
	if submitErr != nil {
		t.Errorf("Submit with custom registry failed: %v", submitErr)
	}
}

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

func Test_SentinelErrors_errorsIsMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		typed    error
		sentinel error
	}{
		{"ClosedError->ErrClosed", &ClosedError{}, ErrClosed},
		{"QueueFullError->ErrQueueFull", &QueueFullError{QueueSize: 100}, ErrQueueFull},
		{"NilProposerError->ErrNilProposer", &NilProposerError{}, ErrNilProposer},
		{"FlushError->ErrFlush", &FlushError{ShardID: 1, Err: errors.New("e")}, ErrFlush},
		{"DecodeError->ErrDecode", &DecodeError{Reason: "r"}, ErrDecode},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !errors.Is(tc.typed, tc.sentinel) {
				t.Errorf("errors.Is(%T, %v) = false, want true", tc.typed, tc.sentinel)
			}
		})
	}
}

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

func Test_SentinelErrors_nonNil(t *testing.T) {
	t.Parallel()
	sentinels := map[string]error{
		"ErrClosed":      ErrClosed,
		"ErrQueueFull":   ErrQueueFull,
		"ErrNilProposer": ErrNilProposer,
		"ErrFlush":       ErrFlush,
		"ErrDecode":      ErrDecode,
	}
	for name, err := range sentinels {
		if err == nil {
			t.Errorf("%s should not be nil", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Typed error aliases
// ---------------------------------------------------------------------------

func Test_TypedErrors_closedErrorAlias(t *testing.T) {
	t.Parallel()
	var err error = &ClosedError{}
	var target *ClosedError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *ClosedError")
	}
}

func Test_TypedErrors_queueFullErrorAlias(t *testing.T) {
	t.Parallel()
	var err error = &QueueFullError{QueueSize: 512}
	var target *QueueFullError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *QueueFullError")
	}
	if target.QueueSize != 512 {
		t.Errorf("QueueSize = %d, want 512", target.QueueSize)
	}
}

func Test_TypedErrors_nilProposerErrorAlias(t *testing.T) {
	t.Parallel()
	var err error = &NilProposerError{}
	var target *NilProposerError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *NilProposerError")
	}
}

func Test_TypedErrors_flushErrorAlias(t *testing.T) {
	t.Parallel()
	inner := errors.New("some cause")
	var err error = &FlushError{ShardID: 42, Err: inner}
	var target *FlushError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *FlushError")
	}
	if target.ShardID != 42 {
		t.Errorf("ShardID = %d, want 42", target.ShardID)
	}
	if target.Err != inner {
		t.Error("inner error mismatch")
	}
}

func Test_TypedErrors_decodeErrorAlias(t *testing.T) {
	t.Parallel()
	var err error = &DecodeError{Reason: "truncated"}
	var target *DecodeError
	if !errors.As(err, &target) {
		t.Fatal("errors.As should match *DecodeError")
	}
	if target.Reason != "truncated" {
		t.Errorf("Reason = %q, want %q", target.Reason, "truncated")
	}
}

func Test_TypedErrors_crossTypeNoMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		// We verify this does NOT match.
		check func(error) bool
	}{
		{
			name:  "ClosedError does not match QueueFullError",
			err:   &ClosedError{},
			check: func(e error) bool { var t *QueueFullError; return errors.As(e, &t) },
		},
		{
			name:  "QueueFullError does not match NilProposerError",
			err:   &QueueFullError{QueueSize: 1},
			check: func(e error) bool { var t *NilProposerError; return errors.As(e, &t) },
		},
		{
			name:  "DecodeError does not match FlushError",
			err:   &DecodeError{Reason: "r"},
			check: func(e error) bool { var t *FlushError; return errors.As(e, &t) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.check(tc.err) {
				t.Error("cross-type errors.As should not match")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Proposer interface compliance
// ---------------------------------------------------------------------------

func Test_MockProposer_implementsProposerInterface(t *testing.T) {
	t.Parallel()
	var _ Proposer = (*mockProposer)(nil)
}

func Test_MockProposer_returnsConfiguredError(t *testing.T) {
	t.Parallel()
	expectedErr := errors.New("test error")
	mp := &mockProposer{err: expectedErr}
	err := mp.SyncPropose(context.Background(), 1, []byte("data"))
	if err != expectedErr {
		t.Errorf("SyncPropose returned %v, want %v", err, expectedErr)
	}
}
