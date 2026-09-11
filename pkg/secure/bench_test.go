// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package secure

import (
	"crypto/rand"
	"testing"
)

// BenchmarkSecret_OpenDestroy exercises the open/destroy cycle for a
// 32-byte AES-class key. Target is <50µs on typical hardware; the
// benchmark is informational only — there is no hard assertion.
func BenchmarkSecret_OpenDestroy(b *testing.B) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		b.Fatalf("rand.Read: %v", err)
	}
	sec, err := NewSecret(key)
	if err != nil {
		b.Fatalf("NewSecret: %v", err)
	}
	b.Cleanup(sec.Destroy)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf, err := sec.Open()
		if err != nil {
			b.Fatalf("Open: %v", err)
		}
		buf.Destroy()
	}
}

// BenchmarkBuffer_NewDestroy measures the per-buffer construction and
// destruction cost. Purely informational.
func BenchmarkBuffer_NewDestroy(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf, err := NewBuffer(32)
		if err != nil {
			b.Fatalf("NewBuffer: %v", err)
		}
		buf.Destroy()
	}
}

// BenchmarkNewSecret measures sealing of a 32-byte key.
func BenchmarkNewSecret(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			b.Fatalf("rand.Read: %v", err)
		}
		sec, err := NewSecret(key)
		if err != nil {
			b.Fatalf("NewSecret: %v", err)
		}
		sec.Destroy()
	}
}

// BenchmarkBorrowGuardDisabled measures what the borrow guard costs the
// production path, which is the disarmed one: a single atomic load in
// rLockBorrow and its partner in rUnlockBorrow.
//
// It is the number that justifies compiling the guard into the ordinary build
// rather than hiding it behind a tag — a tag would put the instrumented code
// and the shipped code in different binaries, and a type's method set must not
// straddle a build constraint.
func BenchmarkBorrowGuardDisabled(b *testing.B) {
	buf, err := NewBuffer(32)
	if err != nil {
		b.Fatalf("NewBuffer: %v", err)
	}
	b.Cleanup(buf.Destroy)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := buf.WithBytes(func([]byte) error { return nil }); err != nil {
			b.Fatalf("WithBytes: %v", err)
		}
	}
}

// BenchmarkBorrowGuardEnabled measures the armed cost — a runtime.Stack call
// per borrow — so the gap between this and BenchmarkBorrowGuardDisabled is on
// the record rather than asserted. It is why the guard is a test facility.
func BenchmarkBorrowGuardEnabled(b *testing.B) {
	buf, err := NewBuffer(32)
	if err != nil {
		b.Fatalf("NewBuffer: %v", err)
	}
	b.Cleanup(buf.Destroy)
	restore := EnableBorrowGuard(func(*NestedBorrowError) {})
	b.Cleanup(restore)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := buf.WithBytes(func([]byte) error { return nil }); err != nil {
			b.Fatalf("WithBytes: %v", err)
		}
	}
}

// BenchmarkCopyOut records the cost of the coordinated copy-out that replaced
// the Size-then-Bytes pair at five call sites, which is one lock acquisition
// rather than two.
func BenchmarkCopyOut(b *testing.B) {
	buf, err := NewBuffer(32)
	if err != nil {
		b.Fatalf("NewBuffer: %v", err)
	}
	b.Cleanup(buf.Destroy)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := CopyOut(buf)
		if err != nil {
			b.Fatalf("CopyOut: %v", err)
		}
		Wipe(out)
	}
}
