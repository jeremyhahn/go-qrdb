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

// Benchmarks for the audit sink's write path and its verifier.
//
// The write path is benchmarked because go-qrdb#452 proposed a fix that would
// have added a second marshal to it -- marshal to size, rotate, recompute the
// digest, re-marshal -- on the sink that every authorization decision goes
// through since go-qrdb#335, and which already fsyncs per write. The fix that
// landed instead is entirely on the read side, so these establish that Emit is
// where it was; run them on both sides of the change to see it.
//
// No build constraint: same reason as chain_rotation_test.go.

package audit

import (
	"context"
	"path/filepath"
	"testing"
)

// benchEvent is a representative authorization record: the shape
// pkg/auth/middleware emits, not a minimal one.
func benchEvent() AuditEvent {
	return AuditEvent{
		Actor:     "CN=node-01.db.example.com",
		TenantID:  "tenant-0123456789abcdef",
		Role:      "admin",
		Resource:  "barrier",
		Action:    "unseal",
		Outcome:   OutcomeSuccess,
		ClientIP:  "203.0.113.17",
		RequestID: "01JDX8T4QK9V2M6B0YH3NZP5RW",
	}
}

// BenchmarkChainFileLogger_Emit is the steady-state write: no rotation, fsync
// per write, which is the shipped default.
func BenchmarkChainFileLogger_Emit(b *testing.B) {
	l, err := NewFileLogger(filepath.Join(b.TempDir(), "audit.ndjson"), WithMaxBytes(1<<40))
	if err != nil {
		b.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	ev := benchEvent()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := l.Emit(ctx, ev); err != nil {
			b.Fatalf("emit: %v", err)
		}
	}
}

// BenchmarkChainFileLogger_Emit_NoSync isolates the marshal-and-chain cost
// from the fsync that dominates the default. It is the measurement a change to
// the chaining logic would move.
func BenchmarkChainFileLogger_Emit_NoSync(b *testing.B) {
	l, err := NewFileLogger(filepath.Join(b.TempDir(), "audit.ndjson"),
		WithMaxBytes(1<<40), WithSyncPerWrite(false))
	if err != nil {
		b.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	ev := benchEvent()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := l.Emit(ctx, ev); err != nil {
			b.Fatalf("emit: %v", err)
		}
	}
}

// BenchmarkChainFileLogger_Emit_Rotating rotates roughly every eight records,
// so the rotation boundary is on the measured path rather than amortised into
// invisibility. A fix that rotated before fixing PrevHash would show up here.
func BenchmarkChainFileLogger_Emit_Rotating(b *testing.B) {
	l, err := NewFileLogger(filepath.Join(b.TempDir(), "audit.ndjson"),
		WithMaxBytes(2048), WithSyncPerWrite(false))
	if err != nil {
		b.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	ev := benchEvent()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := l.Emit(ctx, ev); err != nil {
			b.Fatalf("emit: %v", err)
		}
	}
}

// BenchmarkVerifyChainSet measures the read side that this change added: the
// whole-set walk an auditor runs, over a trail spread across many archives.
// It is off the request path entirely.
func BenchmarkVerifyChainSet(b *testing.B) {
	path := filepath.Join(b.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path, WithMaxBytes(4096), WithSyncPerWrite(false))
	if err != nil {
		b.Fatalf("NewFileLogger: %v", err)
	}
	ctx := context.Background()
	ev := benchEvent()
	const records = 2000
	for i := 0; i < records; i++ {
		if err := l.Emit(ctx, ev); err != nil {
			b.Fatalf("emit: %v", err)
		}
	}
	if err := l.Close(); err != nil {
		b.Fatalf("Close: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v, err := verifyChainSet(path)
		if err != nil {
			b.Fatalf("verifyChainSet: %v", err)
		}
		if v.Records != records {
			b.Fatalf("verified %d of %d records", v.Records, records)
		}
	}
}
