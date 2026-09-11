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

package secure

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// PassphraseProvider reads a passphrase directly into a Buffer, avoiding
// any intermediate Go string. Implementations MUST NOT store the
// passphrase in a heap-allocated string literal at any point. The
// returned Buffer is the caller's responsibility to Destroy — the
// Provider must not retain a reference.
type PassphraseProvider interface {
	// Passphrase prompts for and reads a passphrase. The prompt may be
	// ignored by non-interactive providers. ctx cancellation is respected
	// only on a best-effort basis for terminal reads (the syscall itself
	// may not be interruptible).
	Passphrase(ctx context.Context, prompt string) (*Buffer, error)
}

// TerminalProvider reads a passphrase from /dev/tty via
// golang.org/x/term.ReadPassword, copying the bytes straight into a
// Buffer. The source slice returned by term.ReadPassword is wiped
// before this function returns.
type TerminalProvider struct{}

// NewTerminalProvider returns a TerminalProvider. It is a zero-config
// struct; the function exists for symmetry with the other providers.
func NewTerminalProvider() *TerminalProvider { return &TerminalProvider{} }

// Passphrase reads from os.Stdin (which should be a tty) with echo
// disabled. Returns *ProviderError wrapping the underlying I/O error on
// failure.
func (p *TerminalProvider) Passphrase(ctx context.Context, prompt string) (*Buffer, error) {
	if err := ctx.Err(); err != nil {
		return nil, &ProviderError{Source: "terminal", Err: err}
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, &ProviderError{Source: "terminal", Err: errNotATerminal}
	}
	if prompt != "" {
		if _, err := fmt.Fprint(os.Stderr, prompt); err != nil {
			return nil, &ProviderError{Source: "terminal", Err: err}
		}
	}
	raw, err := term.ReadPassword(fd)
	if err != nil {
		return nil, &ProviderError{Source: "terminal", Err: err}
	}
	// term.ReadPassword returns a newly-allocated slice. Move it into a
	// Buffer and wipe the source.
	if len(raw) == 0 {
		return nil, &ProviderError{Source: "terminal", Err: errEmptyPassphrase}
	}
	// Print newline since echo was off; ignore errors (prompt is cosmetic).
	_, _ = fmt.Fprintln(os.Stderr)
	return NewBufferFromBytes(raw)
}

// MaxStdinPassphraseLen bounds a passphrase read by StdinProvider. The bound
// exists because the read lands directly in a fixed mlocked allocation, and
// mlocked memory is a scarce, RLIMIT_MEMLOCK-governed resource that an
// unbounded stdin stream must not be able to size. Input longer than this
// is refused rather than truncated: silently accepting a prefix would unseal
// against a credential the operator did not type.
const MaxStdinPassphraseLen = 1024

// StdinProvider reads a passphrase from os.Stdin up to the first newline.
// Intended for CI / piped invocations. The trailing newline is stripped.
//
// The read goes one byte at a time straight into mlocked memory. That is not
// a performance choice, and it is deliberately not a bufio.Reader: a
// bufio.Reader stages the passphrase in an ordinary heap buffer that this
// package never gets to wipe, which would leave a plaintext copy behind in
// exactly the provider whose job is to prevent one. Byte-at-a-time is also
// the only shape that does not OVER-READ — a buffered reader consumes past
// the newline, stealing bytes from whatever reads os.Stdin next.
//
// The cost is one read(2) per byte. That is a known, accepted tradeoff on a
// path that runs once per process start and already blocks on a human or a
// pipe; it is not a regression, since bufio made the same blocking reads and
// merely amortized them. If it ever needs amortizing, the shape is a small
// (32-64 byte) mlocked read ring that scans for the newline inside protected
// memory — NOT a return to bufio, which reintroduces both the unwipeable
// staging buffer and the over-read. See go-qrdb#440.
type StdinProvider struct {
	// reader is optional; if nil, os.Stdin is used. Exported for tests.
	reader io.Reader
}

// NewStdinProvider returns a StdinProvider reading from os.Stdin.
func NewStdinProvider() *StdinProvider { return &StdinProvider{} }

// NewStdinProviderFromReader returns a StdinProvider backed by r.
// Primarily used in tests.
func NewStdinProviderFromReader(r io.Reader) *StdinProvider {
	return &StdinProvider{reader: r}
}

// Passphrase reads until newline. Returns *ProviderError on I/O failure,
// empty input, or input longer than MaxStdinPassphraseLen.
//
// Every byte read lands in the scratch Buffer's mlocked pages and nowhere
// else; the scratch is destroyed (and therefore zeroed) on every exit path
// including the error ones, so a rejected or failed read leaves no plaintext
// behind either.
func (p *StdinProvider) Passphrase(ctx context.Context, _ string) (*Buffer, error) {
	if err := ctx.Err(); err != nil {
		return nil, &ProviderError{Source: "stdin", Err: err}
	}
	r := p.reader
	if r == nil {
		r = os.Stdin
	}

	// One mlocked line buffer. Destroy zeroes it, and the defer covers the
	// error returns below as well as the success path.
	scratch, err := NewBuffer(MaxStdinPassphraseLen + 1)
	if err != nil {
		return nil, &ProviderError{Source: "stdin", Err: err}
	}
	defer scratch.Destroy()

	// A bare Bytes() borrow rather than WithBytes: the loop below blocks on
	// a read, and WithBytes would hold purgeMu shared for that whole time,
	// stalling the emergency Purge path behind an operator's typing. The
	// borrow is short-lived in code even when it is long-lived in wall time,
	// and no allocation of protected memory happens inside it.
	line := scratch.Bytes()
	if line == nil {
		return nil, &ProviderError{Source: "stdin", Err: &NilBufferError{}}
	}

	n := 0
	sawNewline := false
	for n < len(line) {
		if _, rerr := io.ReadFull(r, line[n:n+1]); rerr != nil {
			if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
				break
			}
			return nil, &ProviderError{Source: "stdin", Err: rerr}
		}
		if line[n] == '\n' {
			sawNewline = true
			break
		}
		n++
	}
	if n == len(line) {
		return nil, &ProviderError{Source: "stdin", Err: errPassphraseTooLong}
	}
	// A CR is stripped only as the first half of a CRLF. A passphrase that
	// genuinely ends in CR and was terminated by EOF keeps it: truncating
	// there would silently accept a different credential than was supplied.
	if sawNewline && n > 0 && line[n-1] == '\r' {
		n--
	}
	if n == 0 {
		return nil, &ProviderError{Source: "stdin", Err: errEmptyPassphrase}
	}
	// NewBufferFromBytes copies into a fresh enclave and wipes what it read;
	// the scratch's remaining pages are zeroed by the deferred Destroy.
	return NewBufferFromBytes(line[:n])
}

// CallbackProvider wraps a user-supplied function that returns the
// passphrase bytes. Intended for server-side invocations where the
// passphrase arrives over the wire (e.g., a REST handler that lands it in
// a Buffer through a custom json.Unmarshaler). The callback's returned
// slice is consumed and wiped.
type CallbackProvider struct {
	// Fn returns the raw passphrase bytes. It MUST return a newly
	// allocated slice that the Provider is free to wipe.
	Fn func(ctx context.Context, prompt string) ([]byte, error)
}

// NewCallbackProvider returns a CallbackProvider that invokes fn.
func NewCallbackProvider(fn func(ctx context.Context, prompt string) ([]byte, error)) *CallbackProvider {
	return &CallbackProvider{Fn: fn}
}

// Passphrase calls the configured Fn and wraps the result in a Buffer.
// Returns *ProviderError on nil Fn or empty result.
func (p *CallbackProvider) Passphrase(ctx context.Context, prompt string) (*Buffer, error) {
	if p == nil || p.Fn == nil {
		return nil, &ProviderError{Source: "callback", Err: errNilCallback}
	}
	raw, err := p.Fn(ctx, prompt)
	if err != nil {
		return nil, &ProviderError{Source: "callback", Err: err}
	}
	if len(raw) == 0 {
		return nil, &ProviderError{Source: "callback", Err: errEmptyPassphrase}
	}
	return NewBufferFromBytes(raw)
}

// sentinel errors used by providers.
var (
	errNotATerminal      = errors.New("stdin is not a terminal")
	errEmptyPassphrase   = errors.New("empty passphrase")
	errNilCallback       = errors.New("nil callback")
	errPassphraseTooLong = errors.New("passphrase exceeds the maximum stdin length")
)
