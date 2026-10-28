// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !integration

package node

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestNode_ChangePassphrase_NextUnsealsOldRefused changes the passphrase on
// an unsealed node, then reboots it: the next passphrase unseals and the old
// one is refused.
func TestNode_ChangePassphrase_NextUnsealsOldRefused(t *testing.T) {
	ctx := context.Background()
	cfg := sealedStandaloneConfig(t, t.TempDir())

	n1, err := NewStandaloneNode(ctx, cfg, nil, WithSealPassphrase([]byte("old-passphrase")))
	require.NoError(t, err)
	require.NoError(t, n1.ChangePassphrase(ctx,
		seal.MustCredentials(ctx, []byte("old-passphrase")),
		seal.MustCredentials(ctx, []byte("new-passphrase"))))
	require.NoError(t, n1.Close())

	n2, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n2.Close() })
	require.Error(t, n2.Unseal(ctx, seal.MustCredentials(ctx, []byte("old-passphrase"))))
	require.True(t, n2.IsSealed())
	require.NoError(t, n2.Unseal(ctx, seal.MustCredentials(ctx, []byte("new-passphrase"))))
	require.False(t, n2.IsSealed())
}

// TestNode_ChangePassphrase_WrongCurrentIsTyped verifies a current passphrase
// that does not open the barrier is refused with seal.ErrCredentialMismatch.
func TestNode_ChangePassphrase_WrongCurrentIsTyped(t *testing.T) {
	ctx := context.Background()
	cfg := sealedStandaloneConfig(t, t.TempDir())

	n, err := NewStandaloneNode(ctx, cfg, nil, WithSealPassphrase([]byte("right-passphrase")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	err = n.ChangePassphrase(ctx,
		seal.MustCredentials(ctx, []byte("wrong-passphrase")),
		seal.MustCredentials(ctx, []byte("new-passphrase")))
	require.ErrorIs(t, err, seal.ErrCredentialMismatch)
}

// errorRecorder is a slog.Handler that keeps every record at error level or
// above, so a test can assert a flow logged none.
type errorRecorder struct {
	mu      sync.Mutex
	records []string
}

func (r *errorRecorder) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelError
}

func (r *errorRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec.Message)
	return nil
}

func (r *errorRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }

func (r *errorRecorder) WithGroup(string) slog.Handler { return r }

func (r *errorRecorder) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.records...)
}

// TestNode_ChangePassphrase_EmbedderFlow is the embedder's whole path: boot
// a sealed node, initialize the system barrier with Node.Unseal, change the
// passphrase, and reboot. The old passphrase is refused with the typed
// seal.ErrDecryptionFailed, the new one unseals, and nothing on the way
// logs at error level.
func TestNode_ChangePassphrase_EmbedderFlow(t *testing.T) {
	ctx := context.Background()
	cfg := sealedStandaloneConfig(t, t.TempDir())
	rec := &errorRecorder{}
	log := slog.New(rec)

	n1, err := NewStandaloneNode(ctx, cfg, nil, WithLogger(log))
	require.NoError(t, err)
	require.True(t, n1.IsSealed())
	require.NoError(t, n1.Unseal(ctx, seal.MustCredentials(ctx, []byte("first-passphrase"))))
	require.False(t, n1.IsSealed())
	require.NoError(t, n1.ChangePassphrase(ctx,
		seal.MustCredentials(ctx, []byte("first-passphrase")),
		seal.MustCredentials(ctx, []byte("second-passphrase"))))
	require.NoError(t, n1.Close())

	n2, err := NewStandaloneNode(ctx, cfg, nil, WithLogger(log))
	require.NoError(t, err)
	t.Cleanup(func() { _ = n2.Close() })
	err = n2.Unseal(ctx, seal.MustCredentials(ctx, []byte("first-passphrase")))
	require.ErrorIs(t, err, seal.ErrDecryptionFailed)
	var unsealErr *UnsealError
	require.ErrorAs(t, err, &unsealErr)
	require.True(t, n2.IsSealed())
	require.NoError(t, n2.Unseal(ctx, seal.MustCredentials(ctx, []byte("second-passphrase"))))
	require.False(t, n2.IsSealed())

	require.Empty(t, rec.messages(), "the flow logged at error level")
}

// TestNode_ChangePassphrase_NoSeal verifies a node without a seal barrier
// refuses the call with ErrSealNotEnabled.
func TestNode_ChangePassphrase_NoSeal(t *testing.T) {
	n := &Node{}
	err := n.ChangePassphrase(context.Background(), seal.Credentials{}, seal.Credentials{})
	require.ErrorIs(t, err, ErrSealNotEnabled)
}
