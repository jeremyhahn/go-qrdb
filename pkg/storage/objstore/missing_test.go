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

package objstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objmemory "github.com/jeremyhahn/go-objstore/pkg/memory"
)

// plainMissingStore reports a missing object on Get with a plain error, as cloud providers do.
type plainMissingStore struct {
	objcommon.Storage
}

func (s plainMissingStore) Get(key string) (io.ReadCloser, error) {
	rc, err := s.Storage.Get(key)
	if errors.Is(err, objcommon.ErrKeyNotFound) {
		return nil, errors.New("provider: the specified object does not exist")
	}
	return rc, err
}

func TestEngine_MissingWithoutSentinel(t *testing.T) {
	require.NoError(t, RegisterProvider("missing-test-plain-error", ProviderCredentials{},
		func(settings map[string]string, creds objcommon.Credentials) (objcommon.Storage, error) {
			store := objmemory.New()
			if err := store.Configure(settings, creds); err != nil {
				return nil, err
			}
			return plainMissingStore{Storage: store}, nil
		}))

	e, err := New("missing-test-plain-error", map[string]string{})
	require.NoError(t, err, "an empty store without the not-found sentinel opens")
	defer func() { _ = e.Close() }()

	got, err := e.Get([]byte("absent"))
	require.NoError(t, err)
	require.Nil(t, got)

	require.NoError(t, e.Set([]byte("present"), []byte("value")))
	got, err = e.Get([]byte("present"))
	require.NoError(t, err)
	require.Equal(t, []byte("value"), got)

	_, err = e.store.Get("absent")
	require.False(t, notFound(err), "the wrapper must hide the sentinel")
}

func TestMissingIn(t *testing.T) {
	cases := []struct {
		label       string
		name        string
		err         error
		existsFails bool
		want        bool
	}{
		{label: "nil error", name: "absent", err: nil, want: false},
		{label: "sentinel", name: "absent", err: objcommon.ErrKeyNotFound, want: true},
		{label: "wrapped sentinel", name: "absent", err: fmt.Errorf("x: %w", objcommon.ErrKeyNotFound), want: true},
		{label: "plain error on an absent object", name: "absent", err: errProvider, want: true},
		{label: "plain error on a present object", name: "present", err: errProvider, want: false},
		{label: "exists fault never reads as missing", name: "absent", err: errProvider, existsFails: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			_, fs := newFaultEngine(t)
			require.NoError(t, fs.Storage.Put("present", strings.NewReader("v")))
			fs.existsFails = tc.existsFails
			require.Equal(t, tc.want, missingIn(context.Background(), fs, tc.name, tc.err))
		})
	}
}
