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
