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

//go:build qrdb_vlog

package rest

import (
	"errors"
	"net/http"
	"testing"

	restserver "github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/vlogtest"
)

// connectVlogTransport is an SDK REST client of the REAL pkg/api/rest
// server over an in-memory log service.
func connectVlogTransport(t *testing.T) *Transport {
	t.Helper()
	serverTLS, roots := generateRoundTripTLSConfig(t)
	srv := restserver.NewServer(nil, roundTripAdminStub{}, "127.0.0.1:0",
		restserver.WithLogService(vlogtest.NewService(t)),
		restserver.WithTLS(serverTLS),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	return newRoundTripClient(t, "https://"+srv.Addr(), roots)
}

func TestRESTVlog_RoundTripOverTheWire(t *testing.T) {
	vlogtest.Run(t, connectVlogTransport(t))
}

func TestRESTVlog_Guards(t *testing.T) {
	closed := connectVlogTransport(t)
	_ = closed.Close()
	vlogtest.RunGuards(t, connectVlogTransport(t), closed)
}

func TestRESTVlog_NotFoundMapsOnly404(t *testing.T) {
	cause := errors.New("vlog: log a/b/c not found")
	var knf *transport.KeyNotFoundError
	if err := vlogNotFound(&transport.RequestError{Method: "VlogHead", StatusCode: http.StatusNotFound, Err: cause}); !errors.As(err, &knf) || knf.Key != cause.Error() {
		t.Fatalf("404: err = %v (%T), want *transport.KeyNotFoundError carrying the server message", err, err)
	}
	if err := vlogNotFound(&transport.RequestError{Method: "VlogHead", StatusCode: http.StatusNotFound}); !errors.As(err, &knf) || knf.Key != "VlogHead" {
		t.Fatalf("404 without a message: err = %v, want KeyNotFoundError naming the method", err)
	}
	for name, err := range map[string]error{
		"400":   &transport.RequestError{Method: "VlogAppend", StatusCode: http.StatusBadRequest, Err: cause},
		"plain": cause,
	} {
		if got := vlogNotFound(err); got != err {
			t.Errorf("%s: vlogNotFound = %v, want the error unchanged", name, got)
		}
	}
}
