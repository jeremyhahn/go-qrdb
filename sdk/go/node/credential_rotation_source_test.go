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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// credentialRotator is the scheduler's seam on the node's credential service.
type credentialRotator interface {
	CredentialRotateDue(ctx context.Context, now time.Time) (int, error)
}

// TestWithCredentialRotationValueSource_RotatesADuePolicy pins Codex P1-d on
// #1410: an embedded node built with the option reaches the credential core,
// so a due rotation draws its value from the source instead of reporting
// ErrNoRotationValueSource forever.
func TestWithCredentialRotationValueSource_RotatesADuePolicy(t *testing.T) {
	ctx := context.Background()
	var drawn []string
	src := func(_ context.Context, name string) (*secure.Buffer, error) {
		drawn = append(drawn, name)
		return secure.NewBufferFromBytes([]byte("rotated-value"))
	}
	n, err := NewStandaloneNode(ctx, sealedStandaloneConfig(t, t.TempDir()), nil,
		WithSealPassphrase([]byte(nodeSealPass)), WithCredentialRotationValueSource(src))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })

	svc := n.Services().Credential
	if err := svc.CredentialStore(ctx, "db-pass", []byte("initial-value")); err != nil {
		t.Fatalf("CredentialStore: %v", err)
	}
	policy := credentials.RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 1}
	if err := svc.CredentialSetRotationPolicy(ctx, "db-pass", policy); err != nil {
		t.Fatalf("CredentialSetRotationPolicy: %v", err)
	}
	rotator, ok := svc.(credentialRotator)
	if !ok {
		t.Fatalf("node credential service %T has no CredentialRotateDue", svc)
	}
	rotated, err := rotator.CredentialRotateDue(ctx, time.Now().Add(2*time.Hour))
	if errors.Is(err, credentials.ErrNoRotationValueSource) {
		t.Fatalf("the option did not reach the credential core: %v", err)
	}
	if err != nil {
		t.Fatalf("CredentialRotateDue: %v", err)
	}
	if rotated != 1 || len(drawn) != 1 || drawn[0] != "db-pass" {
		t.Fatalf("rotated %d, source drawn for %v; want 1 rotation of db-pass", rotated, drawn)
	}
}
