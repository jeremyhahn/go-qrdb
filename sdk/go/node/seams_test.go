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

package node

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/dek/dektest"
	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// recordingLimiter is a ratelimit.Limiter a composer would supply: it records
// every key it is asked about and answers with allow.
type recordingLimiter struct {
	mu    sync.Mutex
	allow bool
	keys  []string
}

func (l *recordingLimiter) Allow(key string, _ ratelimit.Policy) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys = append(l.keys, key)
	return l.allow, time.Second
}

func (l *recordingLimiter) Close() error { return nil }

func (l *recordingLimiter) asked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.keys)
}

var _ ratelimit.Limiter = (*recordingLimiter)(nil)

// listingSink is a service.BackupSink whose List answers a sentinel, proving a
// backup read went through the composer's sink.
type listingSink struct{ service.BackupSink }

func (listingSink) List(context.Context) ([]service.BackupInfo, error) {
	return []service.BackupInfo{{ID: "from-the-composers-sink"}}, nil
}

func newSeamNode(t *testing.T, serverOpts []server.ServerOption, opts ...NodeOption) *Node {
	t.Helper()
	cfg := newTenantIsolationConfig(t)
	n, err := NewStandaloneNode(context.Background(), cfg, serverOpts, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	return n
}

func TestWithDEKProvider_ServesTheTenantDEKServiceFromTheDevice(t *testing.T) {
	device := dektest.New()
	n := newSeamNode(t, nil, WithDEKProvider(device))
	ctx := context.Background()

	sealed, err := n.Services().DEK.Encrypt(ctx, "", []byte("secret"), []byte("aad"))
	require.NoError(t, err)
	opened, err := n.Services().DEK.Decrypt(ctx, "", sealed.Ciphertext, []byte("aad"))
	require.NoError(t, err)
	require.Equal(t, []byte("secret"), opened.Plaintext)
	require.NotEmpty(t, device.Ops(), "the request never reached the device")
}

// With no provider the service is the software keyring, which needs a key-wrap
// provider and key directory this node was not given: it refuses, where the
// device in the test above served the same request.
func TestWithDEKProvider_AbsentKeepsTheSoftwareKeyring(t *testing.T) {
	n := newSeamNode(t, nil)
	_, err := n.Services().DEK.Encrypt(context.Background(), "", []byte("secret"), nil)
	require.ErrorContains(t, err, `"software" key-wrap provider`)
}

func TestWithBackupSink_ReachesTheBackupService(t *testing.T) {
	n := newSeamNode(t, nil, WithBackupSink(listingSink{}))
	got, err := n.Services().Backup.ListBackups(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "from-the-composers-sink", got[0].ID)
}

func TestWithBackupSink_AbsentLeavesNoSink(t *testing.T) {
	n := newSeamNode(t, nil)
	got, err := n.Services().Backup.ListBackups(context.Background())
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestWithStorageFactory_OpensTheStandaloneDataPlane(t *testing.T) {
	var (
		calls   int
		gotDir  string
		stg     storage.Storage
		factory = func(dataDir string, _ filesys.FS) (storage.Storage, error) {
			calls++
			gotDir = dataDir
			stg = memory.NewStorage()
			return stg, nil
		}
	)
	n := newSeamNode(t, nil, WithStorageFactory(factory))
	require.Equal(t, 1, calls, "the factory must open the data plane exactly once")
	require.NotEmpty(t, gotDir)
	require.NotNil(t, n.Services().KV)
}

func TestWithStorageFactory_ErrorFailsTheBoot(t *testing.T) {
	boom := errors.New("device offline")
	cfg := newTenantIsolationConfig(t)
	_, err := NewStandaloneNode(context.Background(), cfg, nil,
		WithStorageFactory(func(string, filesys.FS) (storage.Storage, error) { return nil, boom }))
	require.ErrorIs(t, err, boom)
}

func TestWithHolderActLimiter_ThrottlesTheEmbeddedHolderClaims(t *testing.T) {
	deny := &recordingLimiter{allow: false}
	n := newSeamNode(t, nil, WithHolderActLimiter(deny))
	_, err := n.Client().Barrier().ShareClaimBegin(context.Background(), &transport.ShareClaimBeginRequest{Username: "nobody"})
	var limited *embedded.HolderActRateLimitedError
	require.ErrorAs(t, err, &limited)
	require.Positive(t, deny.asked(), "the composer's limiter was never consulted")
}

func TestWithInvitationRedeemLimiter_ThrottlesRedemption(t *testing.T) {
	deny := &recordingLimiter{allow: false}
	n := newSeamNode(t, nil, WithInvitationRedeemLimiter(deny))
	require.NotNil(t, n.Services().Invitations)

	_, err := n.Services().Invitations.RedeemInvitation(context.Background(), &service.RedeemInvitationRequest{
		TenantID: "acme", Token: "t", CSRPEM: []byte("csr"),
	})
	var invalid *service.InvitationInvalidError
	require.ErrorAs(t, err, &invalid, "a throttled redemption gets the opaque refusal")
	require.Positive(t, deny.asked(), "the composer's limiter was never consulted")
}

func TestMergeSeamOptions_FoldsNodeOptionsIntoTheServerOptions(t *testing.T) {
	fake := clocktest.New()
	sink := listingSink{}
	device := dektest.New()
	limiter := &recordingLimiter{}
	signers := &deviceHeadSigners{}
	o := &nodeOptions{}
	for _, opt := range []NodeOption{
		WithClock(fake), WithBackupSink(sink), WithDEKProvider(device),
		WithInvitationRedeemLimiter(limiter), WithHolderActLimiter(limiter),
		WithHeadSignerProvider(signers),
	} {
		opt(o)
	}

	got := server.ResolveCryptoOptions(mergeSeamOptions(*o, nil)...)
	require.Equal(t, fake, got.Clock)
	require.Equal(t, sink, got.BackupSink)
	require.Equal(t, device, got.DEKProvider)
	require.Same(t, signers, got.HeadSignerProvider)
	require.Equal(t, limiter, got.InvitationRedeemLimiter)
	require.Equal(t, limiter, got.HolderActLimiter)
	require.Nil(t, got.StorageFactory)
}

func TestMergeSeamOptions_ExplicitServerOptionsWin(t *testing.T) {
	fromNode, fromServer := clocktest.New(), clocktest.New()
	o := &nodeOptions{}
	WithClock(fromNode)(o)

	got := server.ResolveCryptoOptions(mergeSeamOptions(*o, []server.ServerOption{server.WithClock(fromServer)})...)
	require.Same(t, fromServer, got.Clock)
}

func TestWithClock_ReachesTheNode(t *testing.T) {
	fake := clocktest.New()
	n := newSeamNode(t, nil, WithClock(fake))
	require.Same(t, fake, n.clock)
}

// deviceHeadSigners is a HeadSignerProvider standing in for a go-xkms one;
// the node only carries it to the server.
type deviceHeadSigners struct{ calls int }

var _ vlog.HeadSignerProvider = (*deviceHeadSigners)(nil)

func (p *deviceHeadSigners) CreateKey(context.Context, vlog.CreateKeyRequest) (vlog.CreatedKey, error) {
	p.calls++
	return vlog.CreatedKey{}, context.Canceled
}

func (p *deviceHeadSigners) HeadSigner(context.Context, vlog.HeadSignerRequest) (vlog.HeadSigner, error) {
	p.calls++
	return nil, context.Canceled
}

func TestWithHeadSignerProvider_ReachesTheServer(t *testing.T) {
	signers := &deviceHeadSigners{}
	o := &nodeOptions{}
	WithHeadSignerProvider(signers)(o)
	require.Same(t, signers, o.headSignerProvider)
	got := server.ResolveCryptoOptions(mergeSeamOptions(*o, nil)...)
	require.Same(t, signers, got.HeadSignerProvider)
	require.Zero(t, signers.calls, "carrying the provider must not call it")
}

func TestWithHeadSignerProvider_AbsentKeepsTheSoftwareProvider(t *testing.T) {
	got := server.ResolveCryptoOptions(mergeSeamOptions(nodeOptions{}, nil)...)
	require.Nil(t, got.HeadSignerProvider)
}
