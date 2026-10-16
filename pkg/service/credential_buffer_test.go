// Copyright (c) 2026 Jeremy Hahn
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

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// bufferValue reads buf's value inside its own borrow and destroys it.
func bufferValue(t *testing.T, buf *secure.Buffer) string {
	t.Helper()
	require.NotNil(t, buf)
	defer buf.Destroy()
	var value string
	require.NoError(t, buf.WithBytes(func(b []byte) error {
		value = string(b)
		return nil
	}))
	return value
}

func TestCredentialService_CredentialRetrieveBuffer_Manual(t *testing.T) {
	svc := newTestCredentialServiceSvc(t)
	ctx := context.Background()
	require.NoError(t, svc.CredentialStore(ctx, "k", []byte("manual-value")))

	buf, err := svc.CredentialRetrieveBuffer(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, "manual-value", bufferValue(t, buf))
}

func TestCredentialService_CredentialRetrieveBuffer_Barrier(t *testing.T) {
	svc := newTestBarrierCredentialServiceSvc(t)
	ctx := context.Background()
	require.NoError(t, svc.CredentialStore(ctx, "k", []byte("barrier-value")))

	buf, err := svc.CredentialRetrieveBuffer(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, "barrier-value", bufferValue(t, buf))
}

func TestCredentialService_CredentialRetrieveBuffer_NotFound(t *testing.T) {
	svc := newTestBarrierCredentialServiceSvc(t)

	buf, err := svc.CredentialRetrieveBuffer(context.Background(), "absent")
	assert.ErrorIs(t, err, credentials.ErrCredentialNotFound)
	assert.Nil(t, buf)
}

func TestCredentialService_CredentialRetrieveVersionBuffer_CurrentAndGraced(t *testing.T) {
	svc := newTestBarrierCredentialServiceSvc(t)
	ctx := context.Background()
	require.NoError(t, svc.CredentialStore(ctx, "k", []byte("old")))
	_, err := svc.CredentialRotate(ctx, "k", credentials.RotateOptions{
		NewValue:    []byte("new"),
		GracePeriod: time.Hour,
	})
	require.NoError(t, err)

	for _, tc := range []struct {
		version uint64
		want    string
	}{{0, "new"}, {2, "new"}, {1, "old"}} {
		buf, err := svc.CredentialRetrieveVersionBuffer(ctx, "k", tc.version)
		require.NoError(t, err, "version %d", tc.version)
		assert.Equal(t, tc.want, bufferValue(t, buf), "version %d", tc.version)
	}
}

func TestCredentialService_CredentialRetrieveVersionBuffer_UnknownVersion(t *testing.T) {
	svc := newTestBarrierCredentialServiceSvc(t)
	ctx := context.Background()
	require.NoError(t, svc.CredentialStore(ctx, "k", []byte("v")))

	buf, err := svc.CredentialRetrieveVersionBuffer(ctx, "k", 99)
	assert.ErrorIs(t, err, credentials.ErrVersionNotFound)
	assert.Nil(t, buf)
}

func TestCredentialWireValue_CopiesOutAndDestroysBuffer(t *testing.T) {
	buf, err := secure.NewBufferFromBytes([]byte("wire-value"))
	require.NoError(t, err)

	value, err := CredentialWireValue(buf)
	require.NoError(t, err)
	assert.Equal(t, "wire-value", string(value))
	assert.Error(t, buf.WithBytes(func([]byte) error { return nil }),
		"CredentialWireValue must destroy the buffer it copied out of")
}

func TestCredentialWireValue_NilBufferIsAnError(t *testing.T) {
	value, err := CredentialWireValue(nil)
	assert.Error(t, err)
	assert.Nil(t, value)
}

func TestCredentialWireValue_DestroysBufferOnCopyFailure(t *testing.T) {
	buf, err := secure.NewBufferFromBytes([]byte("gone"))
	require.NoError(t, err)
	buf.Destroy()

	value, err := CredentialWireValue(buf)
	assert.Error(t, err, "a destroyed buffer cannot be copied out of")
	assert.Nil(t, value)
}
