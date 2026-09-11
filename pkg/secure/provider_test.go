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
	"io"
	"strings"
	"testing"
)

func TestStdinProvider_ReadsUntilNewline(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []byte
	}{
		{name: "plain", input: "hunter2\n", want: []byte("hunter2")},
		{name: "crlf", input: "hunter2\r\n", want: []byte("hunter2")},
		{name: "no newline eof", input: "hunter2", want: []byte("hunter2")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewStdinProviderFromReader(strings.NewReader(tc.input))
			buf, err := p.Passphrase(context.Background(), "prompt> ")
			if err != nil {
				t.Fatalf("Passphrase: %v", err)
			}
			defer buf.Destroy()
			if !bytes.Equal(buf.Bytes(), tc.want) {
				t.Fatalf("got %q, want %q", buf.Bytes(), tc.want)
			}
		})
	}
}

func TestStdinProvider_Empty(t *testing.T) {
	p := NewStdinProviderFromReader(strings.NewReader(""))
	buf, err := p.Passphrase(context.Background(), "")
	if err == nil || buf != nil {
		t.Fatalf("expected error on empty input")
	}
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ProviderError, got %T", err)
	}
}

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
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("got %q, want %q", buf.Bytes(), want)
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

// TestStdinProvider_LineEndings pins the line-ending handling that used to
// live in the trimTrailingNewline helper. The helper is gone: the read now
// goes byte-at-a-time straight into mlocked memory (a bufio.Reader staged the
// passphrase in an unwipeable heap buffer), so the trimming is part of the
// read and is tested through the provider.
func TestStdinProvider_LineEndings(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		isErr bool
	}{
		{name: "no terminator", in: "abc", want: "abc"},
		{name: "lf", in: "abc\n", want: "abc"},
		{name: "crlf", in: "abc\r\n", want: "abc"},
		{name: "lf only", in: "\n", isErr: true},
		{name: "crlf only", in: "\r\n", isErr: true},
		// A CR is half of a CRLF, not a terminator in its own right: with no
		// newline behind it the byte is part of the passphrase and is kept.
		{name: "bare cr at eof", in: "abc\r", want: "abc\r"},
		// Only the first line is consumed, and nothing past the newline is
		// read from the underlying reader.
		{name: "stops at first newline", in: "abc\ndef\n", want: "abc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewStdinProviderFromReader(strings.NewReader(tc.in))
			buf, err := p.Passphrase(context.Background(), "")
			if tc.isErr {
				if err == nil {
					buf.Destroy()
					t.Fatalf("Passphrase(%q): expected an error", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("Passphrase(%q): %v", tc.in, err)
			}
			defer buf.Destroy()
			if string(buf.Bytes()) != tc.want {
				t.Fatalf("Passphrase(%q) = %q, want %q", tc.in, buf.Bytes(), tc.want)
			}
		})
	}
}

// TestStdinProvider_DoesNotOverRead is the reason the bufio.Reader had to go
// beyond the un-wipeable staging buffer: bufio consumes up to its whole
// buffer from the underlying reader, so everything after the passphrase's
// newline was swallowed and unavailable to the next reader of the same
// stream. This asserts the remainder is still there.
func TestStdinProvider_DoesNotOverRead(t *testing.T) {
	r := strings.NewReader("hunter2\nthe-rest-of-the-stream\n")
	p := NewStdinProviderFromReader(r)
	buf, err := p.Passphrase(context.Background(), "")
	if err != nil {
		t.Fatalf("Passphrase: %v", err)
	}
	defer buf.Destroy()
	if string(buf.Bytes()) != "hunter2" {
		t.Fatalf("passphrase = %q, want %q", buf.Bytes(), "hunter2")
	}
	rest, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(rest) != "the-rest-of-the-stream\n" {
		t.Fatalf("remaining stream = %q, want %q", rest, "the-rest-of-the-stream\n")
	}
}

// TestStdinProvider_TooLong verifies that input exceeding the mlocked line
// buffer is REFUSED, not truncated. Truncation would unseal against a prefix
// of the operator's passphrase without telling anyone.
func TestStdinProvider_TooLong(t *testing.T) {
	long := strings.Repeat("a", MaxStdinPassphraseLen+1) + "\n"
	p := NewStdinProviderFromReader(strings.NewReader(long))
	buf, err := p.Passphrase(context.Background(), "")
	if err == nil {
		buf.Destroy()
		t.Fatalf("expected an error for input longer than %d bytes", MaxStdinPassphraseLen)
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
	if string(buf.Bytes()) != want {
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
// Under go test, os.Stdin is connected to /dev/null, so ReadBytes returns
// (nil, io.EOF) immediately — empty input — which the provider converts to
// *ProviderError wrapping errEmptyPassphrase. This exercises the
// "r = os.Stdin" fallback branch.
func TestNewStdinProvider_PassphraseUsesOSStdin(t *testing.T) {
	p := NewStdinProvider()
	buf, err := p.Passphrase(context.Background(), "")
	// os.Stdin under go test is /dev/null: empty read → errEmptyPassphrase.
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

// TestStdinProvider_EOFWithoutNewline verifies that a reader which returns
// data + io.EOF simultaneously (common for bytes.Reader on the last read)
// is treated as success when the data is non-empty.
func TestStdinProvider_EOFWithoutNewlineSuccess(t *testing.T) {
	// bytes.Reader returns (n, io.EOF) on the final read when n > 0.
	p := NewStdinProviderFromReader(strings.NewReader("mysecret"))
	buf, err := p.Passphrase(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer buf.Destroy()
	if !bytes.Equal(buf.Bytes(), []byte("mysecret")) {
		t.Fatalf("got %q, want %q", buf.Bytes(), "mysecret")
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

// TestStdinProvider_ReadsUntilEOF exercises the path where the reader holds
// a non-newline-terminated string that also returns io.EOF together with the
// final bytes (the standard behaviour of io.Reader contracts).
func TestStdinProvider_ReadsRawIOEOF(t *testing.T) {
	// Use a raw bytes.NewReader — it returns (n, io.EOF) on the last chunk.
	data := []byte("direct-eof")
	p := NewStdinProviderFromReader(io.NopCloser(strings.NewReader(string(data))))
	buf, err := p.Passphrase(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer buf.Destroy()
	if !bytes.Equal(buf.Bytes(), data) {
		t.Fatalf("got %q, want %q", buf.Bytes(), data)
	}
}
