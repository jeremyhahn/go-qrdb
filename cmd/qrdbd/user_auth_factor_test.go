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

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// fakeAuthFactorKV — a minimal in-memory service.KVServicer used only to
// verify buildUserAuthFactorServiceOption's construction path. None of its
// methods are exercised by these tests (service.NewUserAuthFactorService
// only checks that the value is non-nil at construction time); it exists
// purely to satisfy the interface.
// ---------------------------------------------------------------------------

type fakeAuthFactorKV struct{}

func (fakeAuthFactorKV) Put(context.Context, string, []byte) error { return nil }
func (fakeAuthFactorKV) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return true, nil
}
func (fakeAuthFactorKV) Get(context.Context, string) ([]byte, error) { return nil, nil }
func (fakeAuthFactorKV) Delete(context.Context, string) error        { return nil }
func (fakeAuthFactorKV) List(context.Context, string) ([]string, error) {
	return nil, nil
}
func (fakeAuthFactorKV) Scan(context.Context, string) (map[string][]byte, error) {
	return nil, nil
}
func (fakeAuthFactorKV) GetStale(context.Context, string) ([]byte, error) { return nil, nil }
func (fakeAuthFactorKV) ListStale(context.Context, string) ([]string, error) {
	return nil, nil
}
func (fakeAuthFactorKV) ScanStale(context.Context, string) (map[string][]byte, error) {
	return nil, nil
}
func (fakeAuthFactorKV) PutWithMode(context.Context, string, []byte, writemode.Mode) error {
	return nil
}
func (fakeAuthFactorKV) DeleteWithMode(context.Context, string, writemode.Mode) error {
	return nil
}
func (fakeAuthFactorKV) GetWithConsistency(context.Context, string, consistency.ReadOptions) ([]byte, error) {
	return nil, nil
}
func (fakeAuthFactorKV) ListWithConsistency(context.Context, string, consistency.ReadOptions) ([]string, error) {
	return nil, nil
}
func (fakeAuthFactorKV) ScanWithConsistency(context.Context, string, consistency.ReadOptions) (map[string][]byte, error) {
	return nil, nil
}
func (fakeAuthFactorKV) ExistsWithConsistency(context.Context, string, consistency.ReadOptions) (bool, error) {
	return false, nil
}
func (fakeAuthFactorKV) PutEntity(context.Context, *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return &service.PutEntityResponse{}, nil
}
func (fakeAuthFactorKV) GetEntity(context.Context, *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return &service.GetEntityResponse{}, nil
}
func (fakeAuthFactorKV) DeleteEntity(context.Context, *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return &service.DeleteEntityResponse{}, nil
}
func (fakeAuthFactorKV) ListEntities(context.Context, *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return &service.ListEntitiesResponse{}, nil
}

var _ service.KVServicer = fakeAuthFactorKV{}

// ---------------------------------------------------------------------------
// buildUserAuthFactorServiceOption (user_auth_factor.go)
// ---------------------------------------------------------------------------

// TestBuildUserAuthFactorServiceOption_WithKV verifies that a non-nil KV
// servicer yields a working server.ServerOption (the daemon-side
// construction path mirroring sdk/go/node.go's buildLoginOpts) that
// server.New accepts and that starts cleanly with no protocols enabled.
func TestBuildUserAuthFactorServiceOption_WithKV(t *testing.T) {
	opt, err := buildUserAuthFactorServiceOption(fakeAuthFactorKV{})
	require.NoError(t, err)
	require.NotNil(t, opt)

	// No protocols enabled: server.New + Start()/Stop() must accept the
	// option and exit quickly, proving the constructed option is a valid
	// server.ServerOption rather than merely a non-nil closure.
	srv := server.New(&qrdbconfig.Config{}, fakeAuthFactorKV{}, nil, opt)
	require.NoError(t, srv.Start())
	srv.Stop()
}

// TestBuildUserAuthFactorServiceOption_NilKV verifies that a nil KV
// servicer surfaces the typed construction error from
// service.NewUserAuthFactorService instead of silently producing a
// non-functional option.
func TestBuildUserAuthFactorServiceOption_NilKV(t *testing.T) {
	opt, err := buildUserAuthFactorServiceOption(nil)
	assert.Error(t, err)
	assert.Nil(t, opt)
}
