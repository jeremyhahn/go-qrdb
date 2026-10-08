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

package server

import (
	"context"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/dek/dektest"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

type nopLimiter struct{}

func (nopLimiter) Allow(string, ratelimit.Policy) (bool, time.Duration) { return true, 0 }
func (nopLimiter) Close() error                                         { return nil }

type nopSink struct{ service.BackupSink }

// TestResolveCryptoOptions_CompositionSeams: every composition seam an embedder
// sets is surfaced by ResolveCryptoOptions for forwarding into lifecycle.Infra.
func TestResolveCryptoOptions_CompositionSeams(t *testing.T) {
	fake := clocktest.New()
	device := dektest.New()
	limiter := nopLimiter{}
	sink := nopSink{}
	factory := StorageFactory(func(string, vfs.FS) (storage.Storage, error) { return nil, context.Canceled })

	got := ResolveCryptoOptions(
		WithClock(fake),
		WithDEKProvider(device),
		WithBackupSink(sink),
		WithStorageFactory(factory),
		WithInvitationRedeemLimiter(limiter),
		WithHolderActLimiter(limiter),
	)
	if got.Clock != fake {
		t.Error("Clock was not surfaced")
	}
	if got.DEKProvider != device {
		t.Error("DEKProvider was not surfaced")
	}
	if got.BackupSink != sink {
		t.Error("BackupSink was not surfaced")
	}
	if got.StorageFactory == nil {
		t.Error("StorageFactory was not surfaced")
	}
	if got.InvitationRedeemLimiter != limiter || got.HolderActLimiter != limiter {
		t.Error("a limiter was not surfaced")
	}
}

func TestResolveCryptoOptions_SurfacesTheReplicatedAuditLogger(t *testing.T) {
	replicated := &service.OptInAuditLogger{}
	if got := ResolveCryptoOptions(WithReplicatedAuditLogger(replicated)); got.ReplicatedAuditLogger != replicated {
		t.Fatal("the replicated audit logger was not surfaced")
	}
	if got := ResolveCryptoOptions(); got.ReplicatedAuditLogger != nil {
		t.Fatal("a replicated audit logger is set with no option given")
	}
}

// TestResolveCryptoOptions_CompositionSeamsAbsentKeepTheDefaults: with no seam
// set every field is nil, which every consumer reads as the software default.
func TestResolveCryptoOptions_CompositionSeamsAbsentKeepTheDefaults(t *testing.T) {
	got := ResolveCryptoOptions()
	if got.Clock != nil || got.DEKProvider != nil || got.BackupSink != nil ||
		got.StorageFactory != nil || got.InvitationRedeemLimiter != nil || got.HolderActLimiter != nil {
		t.Fatalf("a seam is set with no option given: %+v", got)
	}
}
