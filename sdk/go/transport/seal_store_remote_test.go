// Copyright 2026 Jeremy Hahn
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

package transport

import (
	"context"
	"errors"
	"sort"
	"testing"
)

// fakeSealStoreEndpoints is an in-memory seal store behind the wire calls.
type fakeSealStoreEndpoints struct {
	secrets  map[string][]byte
	resealed []string
	calls    int
}

func newFakeSealStoreEndpoints() *fakeSealStoreEndpoints {
	return &fakeSealStoreEndpoints{secrets: map[string][]byte{}}
}

var errFakeSecretNotFound = errors.New("fake: secret not found")

func (f *fakeSealStoreEndpoints) PutSealStoreSecret(_ context.Context, name string, secret []byte) error {
	f.calls++
	f.secrets[name] = append([]byte(nil), secret...)
	return nil
}

func (f *fakeSealStoreEndpoints) GetSealStoreSecret(_ context.Context, name string) ([]byte, error) {
	f.calls++
	s, ok := f.secrets[name]
	if !ok {
		return nil, errFakeSecretNotFound
	}
	return s, nil
}

func (f *fakeSealStoreEndpoints) DeleteSealStoreSecret(_ context.Context, name string) error {
	f.calls++
	if _, ok := f.secrets[name]; !ok {
		return errFakeSecretNotFound
	}
	delete(f.secrets, name)
	return nil
}

func (f *fakeSealStoreEndpoints) ListSealStoreSecrets(context.Context) ([]string, error) {
	f.calls++
	if len(f.secrets) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(f.secrets))
	for n := range f.secrets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeSealStoreEndpoints) ResealSealStoreSecret(_ context.Context, name string) error {
	f.calls++
	if _, ok := f.secrets[name]; !ok {
		return errFakeSecretNotFound
	}
	f.resealed = append(f.resealed, name)
	return nil
}

func (f *fakeSealStoreEndpoints) GetSealStoreStatus(context.Context) (*SealStoreStatusResponse, error) {
	f.calls++
	return &SealStoreStatusResponse{Available: true, SecretCount: len(f.secrets)}, nil
}

func TestRemoteSealStoreClient_RoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newFakeSealStoreEndpoints()
	c := NewRemoteSealStoreClient(f)

	list, err := c.SealStoreList(ctx)
	if err != nil || list.Names == nil || len(list.Names) != 0 {
		t.Fatalf("empty list = %#v, %v; want non-nil empty", list, err)
	}
	if err := c.SealStorePut(ctx, &SealStorePutRequest{Name: "k", Secret: []byte("v")}); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := c.SealStoreGet(ctx, &SealStoreGetRequest{Name: "k"})
	if err != nil || got.Name != "k" || string(got.Secret) != "v" {
		t.Fatalf("get = %#v, %v", got, err)
	}
	if err := c.SealStoreReseal(ctx, &SealStoreResealRequest{Name: "k"}); err != nil || len(f.resealed) != 1 {
		t.Fatalf("reseal: %v (resealed %v)", err, f.resealed)
	}
	st, err := c.SealStoreStatus(ctx)
	if err != nil || !st.Available || st.SecretCount != 1 {
		t.Fatalf("status = %#v, %v", st, err)
	}
	if err := c.SealStoreDelete(ctx, &SealStoreDeleteRequest{Name: "k"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.SealStoreGet(ctx, &SealStoreGetRequest{Name: "k"}); !errors.Is(err, errFakeSecretNotFound) {
		t.Fatalf("get after delete = %v, want not found", err)
	}
}

func TestRemoteSealStoreClient_RefusesNilAndEmptyBeforeTheWire(t *testing.T) {
	ctx := context.Background()
	f := newFakeSealStoreEndpoints()
	c := NewRemoteSealStoreClient(f)

	nilCalls := map[string]error{
		"Put":    c.SealStorePut(ctx, nil),
		"Delete": c.SealStoreDelete(ctx, nil),
		"Reseal": c.SealStoreReseal(ctx, nil),
	}
	_, nilCalls["Get"] = c.SealStoreGet(ctx, nil)
	for op, err := range nilCalls {
		if !errors.Is(err, ErrSealStoreNilRequest) {
			t.Errorf("%s(nil) = %v, want ErrSealStoreNilRequest", op, err)
		}
	}
	emptyCalls := map[string]error{
		"Put":    c.SealStorePut(ctx, &SealStorePutRequest{}),
		"Delete": c.SealStoreDelete(ctx, &SealStoreDeleteRequest{}),
		"Reseal": c.SealStoreReseal(ctx, &SealStoreResealRequest{}),
	}
	_, emptyCalls["Get"] = c.SealStoreGet(ctx, &SealStoreGetRequest{})
	for op, err := range emptyCalls {
		if !errors.Is(err, ErrSealStoreEmptyName) {
			t.Errorf("%s(empty) = %v, want ErrSealStoreEmptyName", op, err)
		}
	}
	if f.calls != 0 {
		t.Fatalf("wire called %d times for refused requests", f.calls)
	}
}
