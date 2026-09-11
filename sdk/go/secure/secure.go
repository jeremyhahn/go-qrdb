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

// Package secure re-exports go-qrdb's secure-memory types so SDK
// consumers never need to import the internal pkg/secure path or raw
// memguard. All public types and constructors are aliases — there is no
// logic in this file.
//
// See github.com/jeremyhahn/go-qrdb/pkg/secure for the canonical
// documentation.
package secure

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// Secret is the SDK re-export of secure.Secret.
type Secret = secure.Secret

// Buffer is the SDK re-export of secure.Buffer.
type Buffer = secure.Buffer

// PassphraseProvider is the SDK re-export of secure.PassphraseProvider.
type PassphraseProvider = secure.PassphraseProvider

// TerminalProvider is the SDK re-export of secure.TerminalProvider.
type TerminalProvider = secure.TerminalProvider

// StdinProvider is the SDK re-export of secure.StdinProvider.
type StdinProvider = secure.StdinProvider

// CallbackProvider is the SDK re-export of secure.CallbackProvider.
type CallbackProvider = secure.CallbackProvider

// Borrower is the SDK re-export of secure.Borrower — the borrow half of
// *Buffer, and the narrowest thing a reader of protected material needs.
type Borrower = secure.Borrower

// BorrowViolationFunc is the SDK re-export of secure.BorrowViolationFunc.
type BorrowViolationFunc = secure.BorrowViolationFunc

// Error type re-exports.
type (
	NilSecretError      = secure.NilSecretError
	NilBufferError      = secure.NilBufferError
	OpenError           = secure.OpenError
	InvalidSizeError    = secure.InvalidSizeError
	ProviderError       = secure.ProviderError
	NotInitializedError = secure.NotInitializedError
	NestedBorrowError   = secure.NestedBorrowError
	EmptyBorrowError    = secure.EmptyBorrowError
)

// CopyOut is the SDK re-export of secure.CopyOut. It copies protected bytes
// into a fresh caller-owned slice with the borrow held across the whole copy,
// and refuses an empty source rather than returning zeros.
func CopyOut(b Borrower) ([]byte, error) { return secure.CopyOut(b) }

// EnableBorrowGuard is the SDK re-export of secure.EnableBorrowGuard. It arms
// nested-borrow detection so a callback that reaches an allocation of
// protected memory REPORTS instead of deadlocking, and returns a function that
// restores the previous state.
//
// It is a test facility: arming it costs a runtime.Stack call per borrow. It
// is re-exported because a consumer wiring its own protected-memory code
// against this SDK needs the same check the owning repository uses, and cannot
// get it from pkg/secure (SDK consumers never import pkg/*).
func EnableBorrowGuard(onViolation BorrowViolationFunc) (restore func()) {
	return secure.EnableBorrowGuard(onViolation)
}

// GuardBorrows is the SDK re-export of secure.GuardBorrows. It arms the
// nested-borrow detector for the duration of a test and returns a stop
// function that reports the first violation caught, if any:
//
//	defer securesdk.GuardBorrows(t.Fatalf)()
//
// report is t.Fatalf or t.Errorf; nothing in this package imports testing.
//
// DEFERRED, not called at the end of the test. A violation ends the goroutine
// that raised it — including the test's own — so a stop written as the last
// statement is never reached, and the run reports an unexplained Goexit
// instead of the deadlock.
//
// This is the form pkg/secure's package documentation directs callers to, and
// it is re-exported for the reason EnableBorrowGuard is: a consumer following
// that rule against this SDK cannot import pkg/*, and without this would have
// to hand-roll the arm / capture / Goexit / restore sequence — the exact
// footgun the guard exists to remove.
func GuardBorrows(report func(format string, args ...any)) (stop func()) {
	return secure.GuardBorrows(report)
}

// NewSecret is the SDK re-export of secure.NewSecret.
func NewSecret(src []byte) (*Secret, error) { return secure.NewSecret(src) }

// NewSecretFromBuffer is the SDK re-export of secure.NewSecretFromBuffer.
func NewSecretFromBuffer(buf *Buffer) (*Secret, error) {
	return secure.NewSecretFromBuffer(buf)
}

// NewBuffer is the SDK re-export of secure.NewBuffer.
func NewBuffer(size int) (*Buffer, error) { return secure.NewBuffer(size) }

// NewBufferFromBytes is the SDK re-export of secure.NewBufferFromBytes.
func NewBufferFromBytes(src []byte) (*Buffer, error) {
	return secure.NewBufferFromBytes(src)
}

// Wipe is the SDK re-export of secure.Wipe.
func Wipe(b []byte) { secure.Wipe(b) }

// NewTerminalProvider is the SDK re-export of secure.NewTerminalProvider.
func NewTerminalProvider() *TerminalProvider { return secure.NewTerminalProvider() }

// NewStdinProvider is the SDK re-export of secure.NewStdinProvider.
func NewStdinProvider() *StdinProvider { return secure.NewStdinProvider() }

// NewCallbackProvider is the SDK re-export of secure.NewCallbackProvider.
func NewCallbackProvider(fn func(ctx context.Context, prompt string) ([]byte, error)) *CallbackProvider {
	return secure.NewCallbackProvider(fn)
}

// CatchInterrupt is the SDK re-export of secure.CatchInterrupt.
func CatchInterrupt() { secure.CatchInterrupt() }

// EnablePurge is the SDK re-export of secure.EnablePurge. It arms the secure
// subsystem so Purge works WITHOUT installing memguard's process signal
// handler. Daemons that own their own signal handling and drive a graceful
// shutdown must use this instead of CatchInterrupt, whose handler hard
// os.Exit(1)s from inside the signal handler and races ahead of the graceful
// shutdown path (skipping store Close, and so dropping unflushed writes).
func EnablePurge() { secure.EnablePurge() }

// Purge is the SDK re-export of secure.Purge.
func Purge() error { return secure.Purge() }

// PurgeOrPanic is the SDK re-export of secure.PurgeOrPanic.
func PurgeOrPanic() { secure.PurgeOrPanic() }
