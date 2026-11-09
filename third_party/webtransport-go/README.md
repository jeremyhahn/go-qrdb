# webtransport-go (patched in-repo copy)

This is a copy of [github.com/quic-go/webtransport-go](https://github.com/quic-go/webtransport-go)
v0.13.0, with its library sources, unit tests, `internal/testdata` and
LICENSE (MIT). Upstream's `example/`, `integrationtests/`, `interop/`,
`interop_chrome/`, README, SECURITY.md and CI and lint files are not copied. The module path is unchanged. Every
go-qrdb module that lists webtransport-go points at this directory with a
`replace` directive, and keeps the require at v0.13.0.

## Why

- v0.13.0 does not build against quic-go v0.65.0, which go-qrdb uses.
- v0.13.0 buffers streams for a session that is not established yet without
  limit, and every new stream moves the cleanup deadline. A peer can flood a
  connection with streams that are never CONNECTed and keep them buffered
  forever (draft-ietf-webtrans-http3-16 section 4.6 lets an endpoint limit
  this).

Drop this copy, and the `replace` lines, once an upstream release fixes both.

## Patches

1. **quic-go v0.65.0 `HTTPStream`** (`server.go`, `go.mod`). `HTTPStream`
   now returns `(*Stream, error)` and sends the response header only if
   `WriteHeader` came first. `Upgrade` writes the 200 header and then takes
   over the stream. If `HTTPStream` fails, the session is refused with the
   error. A second session without flow control is refused before any header
   is written. The quic-go require is v0.65.0.
2. **Bounded pre-session buffering** (`session_manager.go`). A connection
   buffers at most 16 streams per unknown session
   (`maxBufferedStreamsPerSession`) and 64 in total
   (`maxBufferedStreamsPerConn`). A stream past either cap, or for a session
   that recently closed, is reset at once with WT_BUFFERED_STREAM_REJECTED
   (0x3994bd84). The cleanup deadline starts with the first buffered stream
   for a session and is never renewed.

## Tests

`TestSessionManagerBufferedStreamCaps` and
`TestSessionManagerBufferingDeadlineNotExtended` cover patch 2.
`TestSessionManagerStreamReordering` asserted that each stream renewed the
deadline; its waits now fit inside one deadline. go-qrdb's
`pkg/api/rest` `TestWebTransport_PreSessionStreamFloodIsBounded` floods a
server over raw QUIC.
