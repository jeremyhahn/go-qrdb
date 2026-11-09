# internal/webtransport (patched copy of webtransport-go)

This package is a copy of [github.com/quic-go/webtransport-go](https://github.com/quic-go/webtransport-go)
v0.13.0, with its library sources, unit tests, `internal/testdata` and
LICENSE (MIT); NOTICE names the upstream version and the patches. Upstream's
`example/`, `integrationtests/`, `interop/`, `interop_chrome/`, README,
SECURITY.md, go.mod and CI and lint files are not copied. It is a package of
the go-qrdb module, so every module that builds go-qrdb, including an
embedder of the Go SDK, builds this code; a `replace` directive would apply
only when go-qrdb itself is the main module.

## Why

- v0.13.0 does not build against quic-go v0.65.0, which go-qrdb uses.
- v0.13.0 buffers streams for a session that is not established yet without
  limit, and every new stream moves the cleanup deadline. A peer can flood a
  connection with streams that are never CONNECTed and keep them buffered
  forever (draft-ietf-webtrans-http3-16 section 4.6 lets an endpoint limit
  this).
- v0.13.0 queues every stream an established session's peer opens until the
  application accepts it. Without negotiated flow control nothing bounds that
  queue.

Return to the upstream module once a release fixes all three.

## Patches

1. **quic-go v0.65.0 `HTTPStream`** (`server.go`). `HTTPStream` now returns
   `(*Stream, error)` and sends the response header only if `WriteHeader`
   came first. `Upgrade` writes the 200 header and then takes over the
   stream. If `HTTPStream` fails, the session is refused with the error. A
   second session without flow control is refused before any header is
   written.
2. **Bounded pre-session buffering** (`session_manager.go`). A connection
   buffers at most 16 streams per unknown session
   (`maxBufferedStreamsPerSession`) and 64 in total
   (`maxBufferedStreamsPerConn`). A stream past either cap, or for a session
   that recently closed, is reset at once with WT_BUFFERED_STREAM_REJECTED
   (0x3994bd84). The cleanup deadline starts with the first buffered stream
   for a session and is never renewed.
3. **Bounded accept queue** (`streams_map_incoming.go`, `session.go`,
   `config.go`). At most `Config.MaxIncomingStreams` /
   `MaxIncomingUniStreams` streams (`defaultMaxPendingStreams`, 100, when
   unset) wait for `AcceptStream` / `AcceptUniStream`, whether or not flow
   control was negotiated. A stream past the bound is reset with
   WT_BUFFERED_STREAM_REJECTED and its stream credit returned; the session
   stays up. A stream past a negotiated WT_MAX_STREAMS limit still closes the
   session with WT_FLOW_CONTROL_ERROR.

## Tests

`TestSessionManagerBufferedStreamCaps` and
`TestSessionManagerBufferingDeadlineNotExtended` cover patch 2.
`TestSessionManagerStreamReordering` asserted that each stream renewed the
deadline; its waits now fit inside one deadline.
`TestSessionPendingUniStreamsBounded` covers patch 3. go-qrdb's
`pkg/api/rest` `TestWebTransport_PreSessionStreamFloodIsBounded` floods a
server over raw QUIC, and `TestWebTransport_SessionStreamLimitsConfigured`
holds the REST server to configured session limits.
