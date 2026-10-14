// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package secure

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// newFilledBuffer returns a live Buffer holding a copy of p.
func newFilledBuffer(t *testing.T, p []byte) *Buffer {
	t.Helper()
	b, err := NewBufferFromBytes(append([]byte(nil), p...))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	t.Cleanup(b.Destroy)
	return b
}

func destroyedBuffer(t *testing.T) *Buffer {
	t.Helper()
	b, err := NewBuffer(4)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	b.Destroy()
	return b
}

func wantNilBuffer(t *testing.T, err error) {
	t.Helper()
	var nbe *NilBufferError
	if !errors.As(err, &nbe) {
		t.Fatalf("err = %v (%T), want *NilBufferError", err, err)
	}
}

func wantNilSecret(t *testing.T, err error) {
	t.Helper()
	var nse *NilSecretError
	if !errors.As(err, &nse) {
		t.Fatalf("err = %v (%T), want *NilSecretError", err, err)
	}
}

func TestNewEmptyBuffer_IsLiveAndEmpty(t *testing.T) {
	defer GuardBorrows(t.Fatalf)()
	b := NewEmptyBuffer()
	defer b.Destroy()
	if !b.IsAlive() {
		t.Fatal("IsAlive() = false for a fresh empty Buffer")
	}
	if b.Size() != 0 {
		t.Fatalf("Size() = %d, want 0", b.Size())
	}
	called := false
	if err := b.WithBytes(func(v []byte) error {
		called = true
		if v == nil || len(v) != 0 {
			t.Fatalf("view = %v, want a non-nil empty slice", v)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
	if !called {
		t.Fatal("WithBytes did not call fn on the empty Buffer")
	}
}

func TestNewEmptyBuffer_DestroyEndsIt(t *testing.T) {
	b := NewEmptyBuffer()
	if err := b.DestroyErr(); err != nil {
		t.Fatalf("DestroyErr: %v", err)
	}
	if b.IsAlive() {
		t.Fatal("IsAlive() = true after Destroy")
	}
	wantNilBuffer(t, b.WithBytes(func([]byte) error {
		t.Fatal("fn called on a destroyed empty Buffer")
		return nil
	}))
	if err := b.DestroyErr(); err != nil {
		t.Fatalf("second DestroyErr: %v", err)
	}
}

func TestNewEmptyBuffer_FnErrorPassesThrough(t *testing.T) {
	b := NewEmptyBuffer()
	defer b.Destroy()
	sentinel := errors.New("fn failed")
	if err := b.WithBytes(func([]byte) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the callback's error", err)
	}
}

func TestNewEmptyBuffer_CopyOutRefuses(t *testing.T) {
	b := NewEmptyBuffer()
	defer b.Destroy()
	out, err := CopyOut(b)
	var ebe *EmptyBorrowError
	if !errors.As(err, &ebe) || out != nil {
		t.Fatalf("CopyOut = (%v, %v), want (nil, *EmptyBorrowError)", out, err)
	}
}

func TestBufferWithBytes_NilFn(t *testing.T) {
	wantNilBuffer(t, newFilledBuffer(t, []byte("k")).WithBytes(nil))
	wantNilBuffer(t, NewEmptyBuffer().WithBytes(nil))
}

func TestBufferIsAlive(t *testing.T) {
	tests := []struct {
		name string
		buf  func(t *testing.T) *Buffer
		want bool
	}{
		{name: "live", buf: func(t *testing.T) *Buffer { return newFilledBuffer(t, []byte("key")) }, want: true},
		{name: "live empty", buf: func(*testing.T) *Buffer { return NewEmptyBuffer() }, want: true},
		{name: "destroyed", buf: destroyedBuffer, want: false},
		{name: "nil", buf: func(*testing.T) *Buffer { return nil }, want: false},
		{name: "zero value", buf: func(*testing.T) *Buffer { return &Buffer{} }, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer GuardBorrows(t.Fatalf)()
			if got := tc.buf(t).IsAlive(); got != tc.want {
				t.Fatalf("IsAlive() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBufferEqualBytes(t *testing.T) {
	tests := []struct {
		name string
		buf  func(t *testing.T) *Buffer
		p    []byte
		want bool
	}{
		{name: "equal", buf: func(t *testing.T) *Buffer { return newFilledBuffer(t, []byte("secret")) }, p: []byte("secret"), want: true},
		{name: "same length, different contents", buf: func(t *testing.T) *Buffer { return newFilledBuffer(t, []byte("secret")) }, p: []byte("secreT"), want: false},
		{name: "prefix", buf: func(t *testing.T) *Buffer { return newFilledBuffer(t, []byte("secret")) }, p: []byte("secre"), want: false},
		{name: "longer", buf: func(t *testing.T) *Buffer { return newFilledBuffer(t, []byte("secret")) }, p: []byte("secrets"), want: false},
		{name: "live against empty", buf: func(t *testing.T) *Buffer { return newFilledBuffer(t, []byte("secret")) }, p: nil, want: false},
		{name: "empty against empty", buf: func(*testing.T) *Buffer { return NewEmptyBuffer() }, p: []byte{}, want: true},
		{name: "empty against nil", buf: func(*testing.T) *Buffer { return NewEmptyBuffer() }, p: nil, want: true},
		{name: "empty against non-empty", buf: func(*testing.T) *Buffer { return NewEmptyBuffer() }, p: []byte{0}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer GuardBorrows(t.Fatalf)()
			got, err := tc.buf(t).EqualBytes(tc.p)
			if err != nil {
				t.Fatalf("EqualBytes: %v", err)
			}
			if got != tc.want {
				t.Fatalf("EqualBytes = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBufferEqualBytes_RefusesWhatIsGone(t *testing.T) {
	for name, b := range map[string]*Buffer{
		"nil":        nil,
		"zero value": {},
		"destroyed":  destroyedBuffer(t),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := b.EqualBytes(nil)
			if got {
				t.Fatal("EqualBytes reported equal for a buffer with nothing to compare")
			}
			wantNilBuffer(t, err)
		})
	}
}

func TestBufferEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b func(t *testing.T) *Buffer
		want bool
	}{
		{
			name: "equal",
			a:    func(t *testing.T) *Buffer { return newFilledBuffer(t, []byte("secret")) },
			b:    func(t *testing.T) *Buffer { return newFilledBuffer(t, []byte("secret")) },
			want: true,
		},
		{
			name: "same length, different contents",
			a:    func(t *testing.T) *Buffer { return newFilledBuffer(t, []byte("secret")) },
			b:    func(t *testing.T) *Buffer { return newFilledBuffer(t, []byte("Secret")) },
			want: false,
		},
		{
			name: "different lengths",
			a:    func(t *testing.T) *Buffer { return newFilledBuffer(t, []byte("secret")) },
			b:    func(t *testing.T) *Buffer { return newFilledBuffer(t, []byte("secret!")) },
			want: false,
		},
		{
			name: "two empty",
			a:    func(*testing.T) *Buffer { return NewEmptyBuffer() },
			b:    func(*testing.T) *Buffer { return NewEmptyBuffer() },
			want: true,
		},
		{
			name: "empty against live",
			a:    func(*testing.T) *Buffer { return NewEmptyBuffer() },
			b:    func(t *testing.T) *Buffer { return newFilledBuffer(t, []byte{0}) },
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer GuardBorrows(t.Fatalf)()
			a, b := tc.a(t), tc.b(t)
			for _, pair := range [][2]*Buffer{{a, b}, {b, a}} {
				got, err := pair[0].Equal(pair[1])
				if err != nil {
					t.Fatalf("Equal: %v", err)
				}
				if got != tc.want {
					t.Fatalf("Equal = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestBufferEqual_Self(t *testing.T) {
	defer GuardBorrows(t.Fatalf)()
	b := newFilledBuffer(t, []byte("secret"))
	if got, err := b.Equal(b); err != nil || !got {
		t.Fatalf("Equal(self) = (%v, %v), want (true, nil)", got, err)
	}
	e := NewEmptyBuffer()
	if got, err := e.Equal(e); err != nil || !got {
		t.Fatalf("empty Equal(self) = (%v, %v), want (true, nil)", got, err)
	}
	e.Destroy()
	got, err := e.Equal(e)
	if got {
		t.Fatal("a destroyed Buffer reported equal to itself")
	}
	wantNilBuffer(t, err)
}

func TestBufferEqual_RefusesWhatIsGone(t *testing.T) {
	live := newFilledBuffer(t, []byte("secret"))
	for name, gone := range map[string]*Buffer{
		"nil":        nil,
		"zero value": {},
		"destroyed":  destroyedBuffer(t),
	} {
		t.Run(name, func(t *testing.T) {
			for _, pair := range [][2]*Buffer{{live, gone}, {gone, live}} {
				got, err := pair[0].Equal(pair[1])
				if got {
					t.Fatal("Equal reported equal against a buffer with nothing to compare")
				}
				wantNilBuffer(t, err)
			}
		})
	}
}

func TestNewEmptySecret_IsLiveAndOpensEmpty(t *testing.T) {
	defer GuardBorrows(t.Fatalf)()
	ctx := context.Background()
	s := NewEmptySecret()
	defer s.Destroy()
	if !s.IsAlive() {
		t.Fatal("IsAlive() = false for a fresh empty Secret")
	}
	buf, err := s.Open(ctx)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer buf.Destroy()
	if !buf.IsAlive() || buf.Size() != 0 {
		t.Fatalf("Open = live %v size %d, want a live empty Buffer", buf.IsAlive(), buf.Size())
	}
	called := false
	if err := s.WithBytes(ctx, func(v []byte) error {
		called = true
		if len(v) != 0 {
			t.Fatalf("view length = %d, want 0", len(v))
		}
		return nil
	}); err != nil || !called {
		t.Fatalf("WithBytes = %v, called %v; want nil, true", err, called)
	}
}

func TestNewEmptySecret_DestroyEndsIt(t *testing.T) {
	ctx := context.Background()
	s := NewEmptySecret()
	s.Destroy()
	if s.IsAlive() {
		t.Fatal("IsAlive() = true after Destroy")
	}
	buf, err := s.Open(ctx)
	if buf != nil {
		t.Fatal("Open returned a Buffer for a destroyed empty Secret")
	}
	wantNilSecret(t, err)
	wantNilSecret(t, s.WithBytes(ctx, func([]byte) error {
		t.Fatal("fn called on a destroyed empty Secret")
		return nil
	}))
	s.Destroy()
}

func TestNewSecretFromBuffer_EmptyBuffer(t *testing.T) {
	buf := NewEmptyBuffer()
	s, err := NewSecretFromBuffer(context.Background(), buf)
	if err != nil {
		t.Fatalf("NewSecretFromBuffer: %v", err)
	}
	defer s.Destroy()
	if buf.IsAlive() {
		t.Fatal("the empty Buffer was not consumed")
	}
	if !s.IsAlive() {
		t.Fatal("NewSecretFromBuffer of an empty Buffer did not return a live Secret")
	}
	if err := s.WithBytes(context.Background(), func(v []byte) error {
		if len(v) != 0 {
			t.Fatalf("view length = %d, want 0", len(v))
		}
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
}

func TestNewSecretFromBuffer_DestroyedEmptyBuffer(t *testing.T) {
	buf := NewEmptyBuffer()
	buf.Destroy()
	s, err := NewSecretFromBuffer(context.Background(), buf)
	if s != nil {
		t.Fatal("NewSecretFromBuffer returned a Secret for a destroyed Buffer")
	}
	wantNilBuffer(t, err)
}

func TestSecretWithBytes(t *testing.T) {
	defer GuardBorrows(t.Fatalf)()
	ctx := context.Background()
	s, err := NewSecret(ctx, []byte("sealed value"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	defer s.Destroy()
	var seen bool
	if err := s.WithBytes(ctx, func(v []byte) error {
		seen = bytes.Equal(v, []byte("sealed value"))
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
	if !seen {
		t.Fatal("WithBytes did not hand fn the sealed plaintext")
	}
	// The scoped open leaves the Secret itself intact for the next one.
	if !s.IsAlive() {
		t.Fatal("WithBytes destroyed the Secret")
	}
	sentinel := errors.New("fn failed")
	if err := s.WithBytes(ctx, func([]byte) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the callback's error", err)
	}
}

func TestSecretWithBytes_Refusals(t *testing.T) {
	ctx := context.Background()
	live, err := NewSecret(ctx, []byte("v"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	defer live.Destroy()
	destroyed, err := NewSecret(ctx, []byte("v"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	destroyed.Destroy()

	wantNilSecret(t, live.WithBytes(ctx, nil))
	fn := func([]byte) error {
		t.Fatal("fn called on a Secret with nothing to open")
		return nil
	}
	for name, s := range map[string]*Secret{"nil": nil, "zero value": {}, "destroyed": destroyed} {
		t.Run(name, func(t *testing.T) {
			wantNilSecret(t, s.WithBytes(ctx, fn))
		})
	}
}

func TestSecretWithBytes_CanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s, err := NewSecret(context.Background(), []byte("v"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	defer s.Destroy()
	cancel()
	err = s.WithBytes(ctx, func([]byte) error {
		t.Fatal("fn called under a canceled context")
		return nil
	})
	var oe *OpenError
	if !errors.As(err, &oe) {
		t.Fatalf("err = %v (%T), want *OpenError", err, err)
	}
}

func TestSecretIsAlive(t *testing.T) {
	ctx := context.Background()
	live, err := NewSecret(ctx, []byte("v"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	defer live.Destroy()
	destroyed, err := NewSecret(ctx, []byte("v"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	destroyed.Destroy()
	tests := map[string]struct {
		s    *Secret
		want bool
	}{
		"live":       {s: live, want: true},
		"live empty": {s: NewEmptySecret(), want: true},
		"destroyed":  {s: destroyed, want: false},
		"nil":        {s: nil, want: false},
		"zero value": {s: &Secret{}, want: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := tc.s.IsAlive(); got != tc.want {
				t.Fatalf("IsAlive() = %v, want %v", got, tc.want)
			}
		})
	}
}
