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

package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
)

// certProviderStub is a cert.CertProvider with every method left as the
// embedded nil interface -- enough to exist as a cert.CertProvider value
// without implementing certDeletionWatcher, which is all
// WatchCertProviderEvictions needs: it never calls any OTHER CertProvider
// method, only type-asserts.
type certProviderStub struct {
	cert.CertProvider
}

// certProviderWithWatch adds certDeletionWatcher to certProviderStub, so a
// test can control WatchForDeletions' outcome and observe the ctx it was
// called with.
type certProviderWithWatch struct {
	certProviderStub
	watchErr   error
	watchCalls int
	gotCtx     context.Context
}

func (s *certProviderWithWatch) WatchForDeletions(ctx context.Context) error {
	s.watchCalls++
	s.gotCtx = ctx
	return s.watchErr
}

var _ certDeletionWatcher = (*certProviderWithWatch)(nil)

// TestWatchCertProviderEvictions_NilNodeOrProviderIsNoOp proves both a nil
// Node and a Node with no CertProv configured are documented no-ops.
func TestWatchCertProviderEvictions_NilNodeOrProviderIsNoOp(t *testing.T) {
	if err := WatchCertProviderEvictions(context.Background(), nil); err != nil {
		t.Fatalf("WatchCertProviderEvictions(nil Node) = %v, want nil", err)
	}
	if err := WatchCertProviderEvictions(context.Background(), &Node{}); err != nil {
		t.Fatalf("WatchCertProviderEvictions with a nil CertProv = %v, want nil", err)
	}
}

// TestWatchCertProviderEvictions_ProviderWithoutWatchSupportIsNoOp proves
// an embedder-supplied CertProvider that does not implement
// certDeletionWatcher is left alone rather than failing the type
// assertion loudly.
func TestWatchCertProviderEvictions_ProviderWithoutWatchSupportIsNoOp(t *testing.T) {
	n := &Node{CertProv: certProviderStub{}}
	if err := WatchCertProviderEvictions(context.Background(), n); err != nil {
		t.Fatalf("WatchCertProviderEvictions with a non-watching CertProv = %v, want nil", err)
	}
}

// TestWatchCertProviderEvictions_DelegatesToWatchForDeletions proves a
// CertProvider that DOES implement certDeletionWatcher has
// WatchForDeletions called with the exact ctx passed in, and its result
// returned verbatim.
func TestWatchCertProviderEvictions_DelegatesToWatchForDeletions(t *testing.T) {
	watcher := &certProviderWithWatch{}
	n := &Node{CertProv: watcher}
	ctx := context.Background()

	if err := WatchCertProviderEvictions(ctx, n); err != nil {
		t.Fatalf("WatchCertProviderEvictions: %v", err)
	}
	if watcher.watchCalls != 1 {
		t.Errorf("WatchForDeletions called %d times, want 1", watcher.watchCalls)
	}
	if watcher.gotCtx != ctx {
		t.Error("WatchForDeletions was not called with the exact ctx passed in")
	}

	watcher2 := &certProviderWithWatch{watchErr: errors.New("hub unavailable")}
	n2 := &Node{CertProv: watcher2}
	if err := WatchCertProviderEvictions(ctx, n2); !errors.Is(err, watcher2.watchErr) {
		t.Errorf("WatchCertProviderEvictions = %v, want %v", err, watcher2.watchErr)
	}
}
