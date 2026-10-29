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
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/dek/dektest"
	"github.com/jeremyhahn/go-qrdb/pkg/filesys"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
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
	factory := StorageFactory(func(string, filesys.FS) (storage.Storage, error) { return nil, context.Canceled })

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
// nopHeadSigners is a HeadSignerProvider standing in for a composer's
// hardware one; the option only carries it.
type nopHeadSigners struct{ name string }

func (*nopHeadSigners) CreateKey(context.Context, vlog.CreateKeyRequest) (vlog.CreatedKey, error) {
	return vlog.CreatedKey{}, context.Canceled
}

func (*nopHeadSigners) HeadSigner(context.Context, vlog.HeadSignerRequest) (vlog.HeadSigner, error) {
	return nil, context.Canceled
}

var _ vlog.HeadSignerProvider = (*nopHeadSigners)(nil)

func TestResolveCryptoOptions_SurfacesTheHeadSignerProvider(t *testing.T) {
	device := &nopHeadSigners{name: "hsm"}
	if got := ResolveCryptoOptions(WithHeadSignerProvider(device)); got.HeadSignerProvider != device {
		t.Fatal("the head-signer provider was not surfaced")
	}
}

func TestResolveCryptoOptions_LastHeadSignerProviderWins(t *testing.T) {
	first, second := &nopHeadSigners{name: "a"}, &nopHeadSigners{name: "b"}
	if got := ResolveCryptoOptions(WithHeadSignerProvider(first), WithHeadSignerProvider(second)); got.HeadSignerProvider != second {
		t.Fatal("a later WithHeadSignerProvider did not replace the earlier one")
	}
	if got := ResolveCryptoOptions(WithHeadSignerProvider(first), WithHeadSignerProvider(nil)); got.HeadSignerProvider != nil {
		t.Fatal("WithHeadSignerProvider(nil) did not restore the software default")
	}
}

func TestResolveCryptoOptions_CompositionSeamsAbsentKeepTheDefaults(t *testing.T) {
	got := ResolveCryptoOptions()
	if got.Clock != nil || got.DEKProvider != nil || got.HeadSignerProvider != nil || got.BackupSink != nil ||
		got.StorageFactory != nil || got.InvitationRedeemLimiter != nil || got.HolderActLimiter != nil {
		t.Fatalf("a seam is set with no option given: %+v", got)
	}
}

// settingsWTEnabled is SETTINGS_WT_ENABLED, draft-ietf-webtrans-http3-16
// section 9.2.
const settingsWTEnabled = 0x2c7cf000

// restHTTP3Settings returns the HTTP/3 SETTINGS srv's REST listener sends.
func restHTTP3Settings(t *testing.T, srv *Server) *http3.Settings {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tlsCfg := &tls.Config{RootCAs: testServerCAPool(t), ServerName: "localhost", MinVersion: tls.VersionTLS13, NextProtos: []string{http3.NextProtoH3}}
	conn, err := quic.DialAddr(ctx, srv.RESTAddr(), tlsCfg, &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true})
	if err != nil {
		t.Fatalf("DialAddr: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseWithError(0, "") })
	cc := (&http3.Transport{EnableDatagrams: true}).NewClientConn(conn)
	select {
	case <-cc.ReceivedSettings():
	case <-ctx.Done():
		t.Fatal("no SETTINGS from the REST listener")
	}
	return cc.Settings()
}

func TestWithWebTransportHandler_ReachesTheRESTListener(t *testing.T) {
	handler := func(sess *rest.WebTransportSession, _ *auth.Identity, _ string) { _ = sess.CloseWithError(0, "") }
	for name, tc := range map[string]struct {
		opts []ServerOption
		want uint64
	}{
		"with a handler": {opts: []ServerOption{WithWebTransportHandler("/wt", handler)}, want: 1},
		"without":        {},
	} {
		t.Run(name, func(t *testing.T) {
			srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{}, append([]ServerOption{WithCeremonyCA(testServerCA)}, tc.opts...)...)
			if err := srv.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(srv.Stop)
			if got := restHTTP3Settings(t, srv).Other[settingsWTEnabled]; got != tc.want {
				t.Fatalf("SETTINGS_WT_ENABLED = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestWithWebTransportHandler_InvalidRefusedAtStart(t *testing.T) {
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA), WithWebTransportHandler("/wt", nil))
	err := srv.Start()
	if err == nil {
		srv.Stop()
	}
	var target *rest.InvalidWebTransportHandlerError
	if !errors.As(err, &target) || !target.NilHandler {
		t.Fatalf("Start = %v, want *rest.InvalidWebTransportHandlerError for the nil handler", err)
	}
}
