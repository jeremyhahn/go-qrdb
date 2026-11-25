package webtransport_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/internal/webtransport/internal/testdata"
)

var (
	tlsConf  = testdata.TLSConf
	certPool = testdata.CertPool
)

func newWebTransportRequest(t *testing.T, addr string) *http.Request {
	t.Helper()

	u, err := url.Parse(addr)
	require.NoError(t, err)
	hdr := make(http.Header)
	hdr.Add("Sec-Webtransport-Http3-Draft02", "1")
	return &http.Request{
		Method: http.MethodConnect,
		Header: hdr,
		Proto:  "webtransport-h3",
		Host:   u.Host,
		URL:    u,
	}
}
