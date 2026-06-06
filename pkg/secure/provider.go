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
	"bufio"
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

// StdinProvider reads a passphrase from os.Stdin up to the first newline.
// Intended for CI / piped invocations. The trailing newline is stripped.
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

// Passphrase reads until newline. Returns *ProviderError on I/O failure
// or empty input.
func (p *StdinProvider) Passphrase(ctx context.Context, _ string) (*Buffer, error) {
	if err := ctx.Err(); err != nil {
		return nil, &ProviderError{Source: "stdin", Err: err}
	}
	r := p.reader
	if r == nil {
		r = os.Stdin
	}
	// Use a bufio.Reader sized to hold a single line; this allocates one
	// small buffer but reads bytes through a single copy into our Buffer.
	br := bufio.NewReader(r)
	line, err := br.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, &ProviderError{Source: "stdin", Err: err}
	}
	// Strip trailing \n and \r.
	line = trimTrailingNewline(line)
	if len(line) == 0 {
		return nil, &ProviderError{Source: "stdin", Err: errEmptyPassphrase}
	}
	return NewBufferFromBytes(line)
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
	errNotATerminal    = errors.New("stdin is not a terminal")
	errEmptyPassphrase = errors.New("empty passphrase")
	errNilCallback     = errors.New("nil callback")
)

// trimTrailingNewline returns b with a trailing \n or \r\n removed.
func trimTrailingNewline(b []byte) []byte {
	n := len(b)
	if n > 0 && b[n-1] == '\n' {
		n--
		if n > 0 && b[n-1] == '\r' {
			n--
		}
	}
	return b[:n]
}
