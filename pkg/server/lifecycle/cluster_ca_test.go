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
	"crypto"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// flakyClusterCAProvider is a minimal cakey.Provider double (it reuses
// stubClusterCAProvider's shape via embedding is unnecessary -- this one
// additionally simulates a transient *service.NotLeaderError for a
// configured number of calls before succeeding, and an optional permanent
// failure) for exercising AwaitClusterCACertificate's retry path and
// EstablishClusterCA's wrapping of a permanent one.
type flakyClusterCAProvider struct {
	cert           *x509.Certificate
	notLeaderCalls int   // answers *service.NotLeaderError this many times first
	permanentErr   error // when set, answered immediately, never retried
	calls          int
}

func (f *flakyClusterCAProvider) Signer(context.Context) (crypto.Signer, error) { return nil, nil }

func (f *flakyClusterCAProvider) Certificate(context.Context) (*x509.Certificate, error) {
	f.calls++
	if f.permanentErr != nil {
		return nil, f.permanentErr
	}
	if f.calls <= f.notLeaderCalls {
		return nil, &service.NotLeaderError{ShardID: 1}
	}
	return f.cert, nil
}

// TestAwaitClusterCACertificate_RetriesTransientNotLeader proves the
// exported retry loop survives a transient *service.NotLeaderError and
// succeeds once the provider does, confirming the logic
// cmd/qrdbd's awaitClusterCACertificate now delegates to behaves
// identically from its new, shared home (go-qrdb#591's review, B5).
func TestAwaitClusterCACertificate_RetriesTransientNotLeader(t *testing.T) {
	want := &x509.Certificate{SerialNumber: nil}
	stub := &flakyClusterCAProvider{cert: want, notLeaderCalls: 2}

	got, err := AwaitClusterCACertificate(context.Background(), stub, time.Second, time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("AwaitClusterCACertificate: %v", err)
	}
	if got != want {
		t.Error("expected the certificate returned once the transient NotLeaderError stopped")
	}
	if stub.calls != 3 {
		t.Errorf("Certificate called %d times, want 3 (2 transient failures + 1 success)", stub.calls)
	}
}

// TestAwaitClusterCACertificate_PermanentErrorReturnsImmediately proves a
// non-NotLeaderError is never retried.
func TestAwaitClusterCACertificate_PermanentErrorReturnsImmediately(t *testing.T) {
	boom := errors.New("sealed")
	stub := &flakyClusterCAProvider{permanentErr: boom}

	start := time.Now()
	_, err := AwaitClusterCACertificate(context.Background(), stub, time.Minute, 200*time.Millisecond, time.Second)
	elapsed := time.Since(start)
	if !errors.Is(err, boom) {
		t.Fatalf("AwaitClusterCACertificate = %v, want %v", err, boom)
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("AwaitClusterCACertificate took %s, want an immediate return with no backoff sleep", elapsed)
	}
	if stub.calls != 1 {
		t.Errorf("Certificate called %d times, want exactly 1", stub.calls)
	}
}

// TestEstablishClusterCA_NilProviderIsNoOp proves EstablishClusterCA never
// errors for a composer with no cluster-CA concept at all (BYO-TLS,
// Infra.NoClusterCAKey) -- Node.ClusterCAProv stays nil in that
// configuration, and this must not be mistaken for a failure.
func TestEstablishClusterCA_NilProviderIsNoOp(t *testing.T) {
	if err := EstablishClusterCA(context.Background(), &Node{}); err != nil {
		t.Fatalf("EstablishClusterCA with a nil ClusterCAProv = %v, want nil", err)
	}
	if err := EstablishClusterCA(context.Background(), nil); err != nil {
		t.Fatalf("EstablishClusterCA(nil Node) = %v, want nil", err)
	}
}

// TestEstablishClusterCA_Succeeds proves a normal, already-establishable
// provider resolves without error.
func TestEstablishClusterCA_Succeeds(t *testing.T) {
	stub := &flakyClusterCAProvider{cert: &x509.Certificate{}}
	n := &Node{ClusterCAProv: stub}
	if err := EstablishClusterCA(context.Background(), n); err != nil {
		t.Fatalf("EstablishClusterCA: %v", err)
	}
	if stub.calls != 1 {
		t.Errorf("Certificate called %d times, want 1", stub.calls)
	}
}

// TestEstablishClusterCA_WrapsFailure proves a permanent failure is
// reported as *ClusterCAEstablishError, naming the underlying cause.
func TestEstablishClusterCA_WrapsFailure(t *testing.T) {
	boom := errors.New("barrier sealed")
	stub := &flakyClusterCAProvider{permanentErr: boom}
	n := &Node{ClusterCAProv: stub}

	err := EstablishClusterCA(context.Background(), n)
	var estErr *ClusterCAEstablishError
	if !errors.As(err, &estErr) {
		t.Fatalf("EstablishClusterCA = %v (%T), want *ClusterCAEstablishError", err, err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("EstablishClusterCA error does not unwrap to the underlying cause: %v", err)
	}
}
