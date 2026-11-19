package webtransport

import "io"

const (
	// settingsEnableWebtransportDraft06 is the value for ENABLE_WEBTRANSPORT
	// that was used up until draft-ietf-webtrans-http3-06.
	settingsEnableWebtransportDraft06 = 0x2b603742

	// settingsWebTransportEnabled is the value for SETTINGS_WT_ENABLED.
	settingsWebTransportEnabled = 0x2c7cf000

	// settingsWebTransportMaxSessions is the value for SETTINGS_WT_MAX_SESSIONS.
	settingsWebTransportMaxSessions = 0x14e9cd29

	// settingsWebTransportInitialMaxStreamsUni is the value for SETTINGS_WT_INITIAL_MAX_STREAMS_UNI.
	settingsWebTransportInitialMaxStreamsUni = 0x2b64

	// settingsWebTransportInitialMaxStreamsBidi is the value for SETTINGS_WT_INITIAL_MAX_STREAMS_BIDI.
	settingsWebTransportInitialMaxStreamsBidi = 0x2b65

	// settingsWebTransportInitialMaxData is the value for SETTINGS_WT_INITIAL_MAX_DATA.
	settingsWebTransportInitialMaxData = 0x2b61
)

const (
	// protocolHeader is the Extended-CONNECT :protocol value per
	// draft-ietf-webtrans-http3-16 §3.2, §9.1. It is the only value accepted.
	protocolHeader = "webtransport-h3"
)

// isWebTransportProtocol reports whether s is the draft-16 §3.2 :protocol
// token "webtransport-h3". The pre-draft-15 "webtransport" token is refused.
func isWebTransportProtocol(s string) bool {
	return s == protocolHeader
}

// WebTransport session IDs are the stream IDs of the CONNECT requests that
// establish the sessions. Those requests are sent on client-initiated
// bidirectional streams, whose QUIC stream IDs are divisible by 4.
func isValidSessionID(id uint64) bool {
	return id%4 == 0
}

type byteCountingReader struct {
	io.ByteReader

	BytesRead uint64
}

func (r *byteCountingReader) ReadByte() (byte, error) {
	b, err := r.ByteReader.ReadByte()
	if err == nil {
		r.BytesRead++
	}
	return b, err
}
