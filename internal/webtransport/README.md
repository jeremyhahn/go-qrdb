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
- v0.13.0 can deadlock or leave a stream operation blocked: opening a stream
  at the peer's limit with a full capsule queue, a Read or Write left waiting
  for session shutdown after a local cancel, CancelWrite or Close behind a
  Write blocked sending the stream header, and a header write that session
  shutdown cannot end after a local CancelWrite.
- v0.13.0 keeps one accept notification however many streams arrive, so of
  two concurrent `AcceptStream` callers one can stay blocked while a stream
  waits in the queue.
- v0.13.0 departs from draft-ietf-webtrans-http3-16 section 6 on session
  close: it reports a clean FIN on the CONNECT stream as H3_DATAGRAM_ERROR,
  and truncates a WT_CLOSE_SESSION message instead of rejecting one that is
  over 1024 bytes or not UTF-8.

Return to the upstream module once a release fixes all six.

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
4. **No lock across the stream map** (`session.go`). `OpenStream` and
   `OpenUniStream` no longer hold the session's close lock while opening. At
   the peer's stream limit the map queues WT_STREAMS_BLOCKED, and with a full
   capsule queue that closes the session, which takes the same lock.
5. **Local cancel ends the session-gone wait** (`receive_stream.go`,
   `send_stream.go`). After the peer resets a stream with WT_SESSION_GONE,
   Read and Write wait for the session to close. `CancelRead` and
   `CancelWrite` take the stream out of the session's map, so they now end
   that wait themselves, with the local `StreamError`.
6. **CancelWrite and Close do not wait for the stream header**
   (`send_stream.go`). Write sends the stream header without holding the
   header lock. A `CancelWrite` or `Close` during that send returns at once;
   Write finishes the header, then cancels or closes the stream and returns
   their error without sending its payload.
7. **Every queued stream wakes an acceptor** (`streams_map_incoming.go`).
   The accept queue's notification channel holds one signal. Taking a stream
   off the queue while others remain re-arms it, so a second `AcceptStream`
   or `AcceptUniStream` caller waiting at the same time is woken for the next
   stream.
8. **Session shutdown resets a cancelled stream** (`send_stream.go`).
   Session shutdown resets the send stream with WT_SESSION_GONE even after a
   local `CancelWrite`. That cancel's reset waits for the stream header, so
   only the session's reset ends a header write that never gets credit.
9. **A clean CONNECT FIN is a remote close** (`session.go`). A FIN on the
   CONNECT stream closes the session like WT_CLOSE_SESSION with code 0 and an
   empty message, and the session answers with a FIN, not a reset. A FIN
   inside a capsule is still H3_DATAGRAM_ERROR.
10. **WT_CLOSE_SESSION messages are validated** (`capsule.go`,
    `session.go`). A message over 1024 bytes is rejected before its body is
    read, and one that is not UTF-8 after; either closes the session with
    H3_MESSAGE_ERROR.

## Tests

`TestSessionManagerBufferedStreamCaps` and
`TestSessionManagerBufferingDeadlineNotExtended` cover patch 2.
`TestSessionManagerStreamReordering` asserted that each stream renewed the
deadline; its waits now fit inside one deadline.
`TestSessionPendingUniStreamsBounded` covers patch 3,
`TestSessionOpenStreamAtLimitWithFullCapsuleQueue` patch 4,
`TestReceiveStreamCancelRead*DuringSessionGone` and
`TestSendStreamCancelWrite*DuringSessionGone` patch 5, and
`TestSendStreamStopWhileWriteSendsHeader` patch 6,
`TestIncomingStreamsMapConcurrentAcceptors` patch 7,
`TestSendStreamCancelWriteThenCloseSessionAbortsHeader` patch 8,
`TestConnectStreamFINClosesSessionCleanly` and
`TestConnectStreamFINInsideCapsuleIsAnError` patch 9, and
`TestParseCloseSessionCapsuleRejectsMalformedMessage`,
`TestParseCloseSessionCapsuleAcceptsLongestMessage` and
`TestMalformedCloseSessionMessageClosesSessionWithMessageError` patch 10.
`TestParseCloseSessionCapsuleMessageTruncation` asserted the truncation that
patch 10 replaces. go-qrdb's
`pkg/api/rest` `TestWebTransport_PreSessionStreamFloodIsBounded` floods a
server over raw QUIC, and `TestWebTransport_SessionStreamLimitsConfigured`
holds the REST server to configured session limits.
