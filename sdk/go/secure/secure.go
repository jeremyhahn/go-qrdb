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

// Error type re-exports.
type (
	NilSecretError      = secure.NilSecretError
	NilBufferError      = secure.NilBufferError
	OpenError           = secure.OpenError
	InvalidSizeError    = secure.InvalidSizeError
	ProviderError       = secure.ProviderError
	NotInitializedError = secure.NotInitializedError
)

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

// Purge is the SDK re-export of secure.Purge.
func Purge() error { return secure.Purge() }

// PurgeOrPanic is the SDK re-export of secure.PurgeOrPanic.
func PurgeOrPanic() { secure.PurgeOrPanic() }
