package server

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/quic-go/webtransport-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
)

// TestWithWebTransportHandler_EmptyPathNilHandlerRefusedAtStart pins that an
// explicitly supplied ("", nil) reaches REST's validation: Start refuses it
// rather than starting with WebTransport off.
func TestWithWebTransportHandler_EmptyPathNilHandlerRefusedAtStart(t *testing.T) {
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA), WithWebTransportHandler("", nil))
	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("Start accepted WithWebTransportHandler(\"\", nil)")
	}
	var e *rest.InvalidWebTransportHandlerError
	if !errors.As(err, &e) {
		t.Fatalf("Start = %v, want *rest.InvalidWebTransportHandlerError", err)
	}
	if e.Path != "" || !e.NilHandler {
		t.Fatalf("refusal = %+v, want the empty path and the nil handler", e)
	}
}

// TestWithWebTransportHandler_OmittedLeavesWebTransportOff pins that without
// the option Start succeeds and the REST HTTP/3 listener offers no
// WebTransport: a client's dial is refused.
func TestWithWebTransportHandler_OmittedLeavesWebTransportOff(t *testing.T) {
	srv := New(testConfig(t, true, true), newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	if srv.opts.webTransportSet {
		t.Fatal("webTransportSet is true without the option")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d := &webtransport.Transport{TLSClientConfig: &tls.Config{
		RootCAs:    testServerCAPool(t),
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"h3"},
	}}
	t.Cleanup(func() { _ = d.Close() })
	resp, sess, err := d.Dial(ctx, "https://"+srv.RESTAddr()+"/wt", http.Header{})
	if sess != nil {
		_ = sess.CloseWithError(0, "")
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("a WebTransport session opened with the option omitted")
	}
}
