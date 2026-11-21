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
	"os"
	"strings"
	"testing"
)

func TestStdinProvider_CtxCanceled(t *testing.T) {
	p := NewStdinProviderFromReader(strings.NewReader("anything\n"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	buf, err := p.Passphrase(ctx, "")
	if err == nil || buf != nil {
		t.Fatalf("expected error on canceled ctx")
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProviderError, got %T", err)
	}
}

func TestCallbackProvider(t *testing.T) {
	want := []byte("callback-secret")
	p := NewCallbackProvider(func(_ context.Context, _ string) ([]byte, error) {
		// Return a fresh allocation since the provider will wipe it.
		out := make([]byte, len(want))
		copy(out, want)
		return out, nil
	})
	buf, err := p.Passphrase(context.Background(), "ignored")
	if err != nil {
		t.Fatalf("Passphrase: %v", err)
	}
	defer buf.Destroy()
	if !bytes.Equal(contents(buf), want) {
		t.Fatalf("got %q, want %q", contents(buf), want)
	}
}

func TestCallbackProvider_NilFn(t *testing.T) {
	p := &CallbackProvider{}
	buf, err := p.Passphrase(context.Background(), "")
	if err == nil || buf != nil {
		t.Fatalf("expected error")
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProviderError, got %T", err)
	}
}

func TestCallbackProvider_FnErr(t *testing.T) {
	sentinel := errors.New("boom")
	p := NewCallbackProvider(func(_ context.Context, _ string) ([]byte, error) {
		return nil, sentinel
	})
	_, err := p.Passphrase(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected to wrap sentinel, got %v", err)
	}
}

func TestCallbackProvider_FnEmpty(t *testing.T) {
	p := NewCallbackProvider(func(_ context.Context, _ string) ([]byte, error) {
		return nil, nil
	})
	_, err := p.Passphrase(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error for empty passphrase")
	}
}

func TestTerminalProvider_NonTTY(t *testing.T) {
	// go test pipes stdin — os.Stdin is not a tty in this environment.
	// We just assert we get a *ProviderError rather than panic.
	p := NewTerminalProvider()
	_, err := p.Passphrase(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error when stdin is not a tty")
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProviderError, got %T", err)
	}
}

// TestStdinProvider_AtMaxLength verifies the boundary is inclusive: a
// passphrase of exactly MaxStdinPassphraseLen bytes is accepted.
func TestStdinProvider_AtMaxLength(t *testing.T) {
	want := strings.Repeat("a", MaxStdinPassphraseLen)
	p := NewStdinProviderFromReader(strings.NewReader(want + "\n"))
	buf, err := p.Passphrase(context.Background(), "")
	if err != nil {
		t.Fatalf("Passphrase at exactly MaxStdinPassphraseLen: %v", err)
	}
	defer buf.Destroy()
	if string(contents(buf)) != want {
		t.Fatalf("length = %d, want %d", buf.Size(), MaxStdinPassphraseLen)
	}
}

// TestNewStdinProvider verifies the exported constructor returns a non-nil
// provider backed by os.Stdin (the nil reader field is resolved lazily).
func TestNewStdinProvider(t *testing.T) {
	p := NewStdinProvider()
	if p == nil {
		t.Fatalf("NewStdinProvider() returned nil")
	}
	// The lazy-init sentinel: reader field is nil and will fall back to os.Stdin.
	if p.reader != nil {
		t.Fatalf("expected nil reader field, got %v", p.reader)
	}
}

// TestNewStdinProvider_PassphraseUsesOSStdin verifies that a StdinProvider
// constructed via NewStdinProvider() (reader == nil) falls back to os.Stdin.
// The test points os.Stdin at a pipe whose write end is already closed, so
// the read sees io.EOF immediately — empty input — which the provider converts
// to *ProviderError wrapping errEmptyPassphrase. This exercises the
// "r = os.Stdin" fallback branch without depending on the process's inherited
// stdin, which is an open terminal or pipe whenever the test binary runs
// outside `go test` (a coverage run, a background job) and would block.
func TestNewStdinProvider_PassphraseUsesOSStdin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing the pipe's write end: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		_ = r.Close()
	})

	p := NewStdinProvider()
	buf, err := p.Passphrase(context.Background(), "")
	if err == nil || buf != nil {
		t.Fatalf("expected *ProviderError from empty stdin, got buf=%v err=nil", buf)
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProviderError, got %T: %v", err, err)
	}
}

// errReader is an io.Reader that returns a non-EOF error on the first Read.
type errReader struct{ err error }

func (r *errReader) Read(_ []byte) (int, error) { return 0, r.err }

// TestStdinProvider_IOError exercises the branch in StdinProvider.Passphrase
// where the byte-at-a-time read returns an error that is NOT io.EOF (nor
// io.ErrUnexpectedEOF). That path wraps the error in *ProviderError and
// returns it to the caller.
func TestStdinProvider_IOError(t *testing.T) {
	injected := errors.New("injected read error")
	p := NewStdinProviderFromReader(&errReader{err: injected})
	buf, err := p.Passphrase(context.Background(), "")
	if err == nil || buf != nil {
		t.Fatalf("expected error on I/O failure")
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProviderError, got %T", err)
	}
	if !errors.Is(err, injected) {
		t.Fatalf("expected wrapped injected error, got %v", err)
	}
}

// TestTerminalProvider_CtxCanceled verifies that TerminalProvider.Passphrase
// returns *ProviderError immediately when the context is already cancelled,
// without attempting any terminal I/O.
func TestTerminalProvider_CtxCanceled(t *testing.T) {
	p := NewTerminalProvider()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	buf, err := p.Passphrase(ctx, "")
	if err == nil || buf != nil {
		t.Fatalf("expected error on cancelled context")
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProviderError, got %T", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected wrapped context.Canceled, got %v", err)
	}
}

// TestCallbackProvider_NilReceiver verifies that a nil *CallbackProvider
// returns *ProviderError rather than panicking.
func TestCallbackProvider_NilReceiver(t *testing.T) {
	var p *CallbackProvider
	buf, err := p.Passphrase(context.Background(), "")
	if err == nil || buf != nil {
		t.Fatalf("expected error from nil receiver")
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProviderError, got %T", err)
	}
}

// TestStdinProvider_TakesNoNestedBorrow pins the single, sequential ownership
// in StdinProvider.Passphrase: the line is read under scratch's borrow, and the
// returned Buffer is allocated only after that borrow ends. An earlier form
// allocated the result from inside scratch's borrow, handing scratch's own
// pages to a constructor that wipes its source — a nested borrow the guard
// reports.
func TestStdinProvider_TakesNoNestedBorrow(t *testing.T) {
	defer GuardBorrows(t.Fatalf)()

	buf, err := NewStdinProviderFromReader(strings.NewReader("correct horse\r\nleftover")).
		Passphrase(context.Background(), "")
	if err != nil {
		t.Fatalf("Passphrase: %v", err)
	}
	defer buf.Destroy()
	out, err := CopyOut(buf)
	if err != nil {
		t.Fatalf("CopyOut: %v", err)
	}
	if string(out) != "correct horse" {
		t.Errorf("passphrase = %q, want %q", out, "correct horse")
	}
}

// TestCopyPrefix_RefusesADestroyedSource pins copyPrefix's failure mapping: a
// source destroyed before the copy is a *NilBufferError, and dst is untouched.
func TestCopyPrefix_RefusesADestroyedSource(t *testing.T) {
	dst, err := NewBuffer(4)
	if err != nil {
		t.Fatalf("NewBuffer(dst): %v", err)
	}
	defer dst.Destroy()
	src, err := NewBufferFromBytes([]byte("abcd"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes(src): %v", err)
	}
	src.enc.Destroy() // gone underneath the wrapper, as after a WipeAll

	var nilBuf *NilBufferError
	if err := copyPrefix(dst, src, 4); !errors.As(err, &nilBuf) {
		t.Fatalf("copyPrefix from a destroyed source: %v, want *NilBufferError", err)
	}
	if got := contents(dst); !bytes.Equal(got, make([]byte, 4)) {
		t.Errorf("dst = %q after a refused copy, want zeros", got)
	}
}
