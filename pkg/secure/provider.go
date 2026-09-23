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

	// Ownership is single and sequential: scratch belongs to this function for
	// its whole life, the returned Buffer is allocated only after the read has
	// finished, and no protected allocation happens while any borrow is held.
	//
	// The read runs inside scratch's own WithBytes, held for the WHOLE loop,
	// because scratch is on go-enclave's shutdown ledger the moment NewBuffer
	// returns: a concurrent WipeAll needs no reference to it to unmap it, and
	// a bare Bytes() alias could fault mid-read (go-qrdb#114 review, round 4).
	// Holding it costs shutdown nothing — WipeAll's sweep uses TryLock, so it
	// skips a held enclave rather than waiting on an operator still typing.
	var n int
	var opened bool
	if err := scratch.WithBytes(func(line []byte) error {
		opened = true
		var rerr error
		n, rerr = readStdinLine(r, line)
		return rerr
	}); err != nil {
		if !opened {
			return nil, &ProviderError{Source: "stdin", Err: &NilBufferError{}}
		}
		return nil, err
	}

	result, err := NewBuffer(n)
	if err != nil {
		return nil, &ProviderError{Source: "stdin", Err: err}
	}
	if err := copyPrefix(result, scratch, n); err != nil {
		result.Destroy()
		return nil, &ProviderError{Source: "stdin", Err: err}
	}
	// scratch's pages, the only other copy, are wiped by the deferred Destroy.
	return result, nil
}

// readStdinLine reads one byte at a time from r into line until a newline,
// EOF, or line is full, and returns the passphrase length. It never reads past
// the newline, so the rest of r is left for its next consumer.
//
// A CR is stripped only as the first half of a CRLF. A passphrase that
// genuinely ends in CR and was terminated by EOF keeps it: truncating there
// would silently accept a different credential than was supplied.
func readStdinLine(r io.Reader, line []byte) (int, error) {
	n := 0
	sawNewline := false
	for n < len(line) {
		if _, rerr := io.ReadFull(r, line[n:n+1]); rerr != nil {
			if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
				break
			}
			return 0, &ProviderError{Source: "stdin", Err: rerr}
		}
		if line[n] == '\n' {
			sawNewline = true
			break
		}
		n++
	}
	if n == len(line) {
		return 0, &ProviderError{Source: "stdin", Err: errPassphraseTooLong}
	}
	if sawNewline && n > 0 && line[n-1] == '\r' {
		n--
	}
	if n == 0 {
		return 0, &ProviderError{Source: "stdin", Err: errEmptyPassphrase}
	}
	return n, nil
}

// copyPrefix copies the first n bytes of src into dst, with dst's borrow and
// src's go-enclave borrow both held for the whole copy so a concurrent
// WipeAll can destroy neither mid-copy. It allocates nothing. The inner borrow
// is taken on src's enclave directly rather than through src.WithBytes: dst
// and src are different enclaves with independent locks, so holding both
// cannot deadlock, and going through the enclave keeps the borrow guard's
// one-secure-borrow-at-a-time rule meaningful for callers.
func copyPrefix(dst, src *Buffer, n int) error {
	return dst.WithBytes(func(d []byte) error {
		return nilBufferIfGone(src.enc.WithBytes(func(s []byte) error {
			copy(d, s[:n])
			return nil
		}))
	})
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
