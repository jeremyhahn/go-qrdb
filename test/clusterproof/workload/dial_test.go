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

//go:build clusterproof

package workload

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// stubTransport is a transport.Transport that records how it was built and
// whether it was closed.
type stubTransport struct {
	cfg        transport.Config
	connectErr error
	closed     bool
}

func (s *stubTransport) Connect(context.Context) error                   { return s.connectErr }
func (s *stubTransport) Close() error                                    { s.closed = true; return nil }
func (s *stubTransport) Healthy(context.Context) bool                    { return s.connectErr == nil }
func (s *stubTransport) Request(context.Context, string, any, any) error { return nil }

var _ transport.Transport = (*stubTransport)(nil)

// guardedStub is a stubTransport that also carries the guarded KV verbs. The
// embedded interface is nil: the dial tests never call a KV method.
type guardedStub struct {
	*stubTransport
	transport.GuardedKVClient
}

// Close resolves the ambiguity between the two embedded Close methods.
func (g guardedStub) Close() error { return g.stubTransport.Close() }

// withTransport installs ctor under protocol for the duration of the test.
func withTransport(t *testing.T, protocol string, ctor transportCtor) {
	t.Helper()
	prev, had := transports[protocol]
	transports[protocol] = ctor
	t.Cleanup(func() {
		if had {
			transports[protocol] = prev
		} else {
			delete(transports, protocol)
		}
	})
}

// stubCtor returns a constructor building a transport by wrap, and the last
// stub it built.
func stubCtor(connectErr error, guarded bool) (transportCtor, func() *stubTransport) {
	var last *stubTransport
	ctor := func(opts ...transport.Option) (transport.Transport, error) {
		last = &stubTransport{connectErr: connectErr}
		for _, o := range opts {
			o(&last.cfg)
		}
		if guarded {
			return guardedStub{stubTransport: last}, nil
		}
		return last, nil
	}
	return ctor, func() *stubTransport { return last }
}

func dialConfig(protocol string, creds Credentials) Config {
	return Config{Protocol: protocol, Credentials: creds, OpTimeout: Duration(3 * time.Second)}
}

func TestNewTransportDialerKnownProtocols(t *testing.T) {
	for _, p := range []string{"quic", "grpc", "rest"} {
		if _, err := NewTransportDialer(dialConfig(p, Credentials{})); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
}

func TestNewTransportDialerUnknownProtocol(t *testing.T) {
	for _, p := range []string{"", "http", "QUIC"} {
		_, err := NewTransportDialer(dialConfig(p, Credentials{}))
		var upe *UnknownProtocolError
		if !errors.As(err, &upe) || upe.Protocol != p {
			t.Fatalf("protocol %q: err = %v, want UnknownProtocolError", p, err)
		}
		if !strings.Contains(err.Error(), "quic, grpc or rest") {
			t.Fatalf("message %q does not name the choices", err)
		}
	}
}

func TestTransportDialerPassesConfiguration(t *testing.T) {
	ctor, last := stubCtor(nil, true)
	withTransport(t, "stub", ctor)
	creds := Credentials{CAFile: "ca.pem", CertFile: "client.pem", KeyFile: "client-key.pem"}
	dial, err := NewTransportDialer(dialConfig("stub", creds))
	if err != nil {
		t.Fatalf("NewTransportDialer: %v", err)
	}
	conn, err := dial(context.Background(), "db.example.com:7000")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	got := last().cfg
	want := transport.Config{
		Address:     "db.example.com:7000",
		TLSEnabled:  true,
		TLSCAFile:   "ca.pem",
		TLSCertFile: "client.pem",
		TLSKeyFile:  "client-key.pem",
		Timeout:     3 * time.Second,
	}
	if got != want {
		t.Fatalf("transport config = %+v, want %+v", got, want)
	}
	if err := conn.Close(); err != nil || !last().closed {
		t.Fatalf("Close did not close the transport (err %v)", err)
	}
}

func TestTransportDialerWithoutCredentialsLeavesTLSToTheTransport(t *testing.T) {
	ctor, last := stubCtor(nil, true)
	withTransport(t, "stub", ctor)
	dial, err := NewTransportDialer(dialConfig("stub", Credentials{}))
	if err != nil {
		t.Fatalf("NewTransportDialer: %v", err)
	}
	if _, err := dial(context.Background(), "db.example.com:7000"); err != nil {
		t.Fatalf("dial: %v", err)
	}
	if last().cfg.TLSEnabled {
		t.Fatalf("TLS enabled with no credentials configured: %+v", last().cfg)
	}
}

func TestTransportDialerConnectFailure(t *testing.T) {
	refused := &transport.ConnectionError{Address: "db.example.com:7000", Err: context.DeadlineExceeded}
	ctor, last := stubCtor(refused, true)
	withTransport(t, "stub", ctor)
	dial, err := NewTransportDialer(dialConfig("stub", Credentials{}))
	if err != nil {
		t.Fatalf("NewTransportDialer: %v", err)
	}
	_, err = dial(context.Background(), "db.example.com:7000")
	var de *DialError
	if !errors.As(err, &de) || de.Endpoint != "db.example.com:7000" || !errors.Is(err, refused) {
		t.Fatalf("err = %v, want DialError wrapping the connect failure", err)
	}
	if !last().closed {
		t.Fatal("transport left open after a failed connect")
	}
}

func TestTransportDialerConstructorFailure(t *testing.T) {
	bad := &transport.ConfigError{Field: "Address", Reason: "required"}
	withTransport(t, "stub", func(...transport.Option) (transport.Transport, error) { return nil, bad })
	dial, err := NewTransportDialer(dialConfig("stub", Credentials{}))
	if err != nil {
		t.Fatalf("NewTransportDialer: %v", err)
	}
	_, err = dial(context.Background(), "db.example.com:7000")
	var de *DialError
	if !errors.As(err, &de) || !errors.Is(err, bad) {
		t.Fatalf("err = %v, want DialError wrapping the constructor failure", err)
	}
}

func TestTransportDialerRequiresGuardedVerbs(t *testing.T) {
	ctor, last := stubCtor(nil, false)
	withTransport(t, "stub", ctor)
	dial, err := NewTransportDialer(dialConfig("stub", Credentials{}))
	if err != nil {
		t.Fatalf("NewTransportDialer: %v", err)
	}
	_, err = dial(context.Background(), "db.example.com:7000")
	var ute *UnsupportedTransportError
	if !errors.As(err, &ute) || ute.Protocol != "stub" {
		t.Fatalf("err = %v, want UnsupportedTransportError", err)
	}
	if !last().closed {
		t.Fatal("transport left open after it was refused")
	}
}

func TestErrorMessages(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"dial":        {&DialError{Endpoint: "db.example.com:7000", Err: context.Canceled}, "workload: dial db.example.com:7000: context canceled"},
		"unsupported": {&UnsupportedTransportError{Protocol: "rest"}, `workload: transport "rest" does not support compare-and-swap`},
		"final read":  {&FinalReadError{Keys: []string{"a", "b"}}, "workload: final read: no endpoint answered for 2 key(s): a, b"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}
