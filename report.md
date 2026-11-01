# wt1243h report: #1316, Codex review 8950 P1

## Finding
Before any server change, on a handshake that differed from the valid
draft-16 baseline in one field only, webtransport-go v0.13.0's Upgrade
**opened a session** (200, handler ran) for a client:
- without SETTINGS_WT_ENABLED, and
- without the reset_stream_at transport parameter.

Draft-16 section 3.1 says the server MUST treat such sessions as malformed, so
serveWebTransport (pkg/api/rest/webtransport.go) now refuses them itself with
400 and `WebTransportClientRequirementError` (pkg/api/rest/errors.go, the
package's error file; outside the brief's file map, added to match where
every other WebTransport error lives). The guard reaches the QUIC connection
through a ConnContext chained in newWebTransportServer (quic-go v0.64 http3
has no Hijacker), and the client's SETTINGS through http3.Settingser.

## Discrimination table (step 4)

| case | guard | fails without it |
|------|-------|------------------|
| no WT_ENABLED | webTransportClientRequirements[SETTINGS_WT_ENABLED] (ours) | y: 200, handler ran |
| no reset_stream_at | webTransportClientRequirements[reset_stream_at] (ours) | y: 200, handler ran |
| other protocol (connect-udp) | webtransport-go Upgrade :protocol check (upstream) | not removable (read-only module cache); baseline differs in :protocol only and gets 200 |
| unsupported version (webtransport-h4) | same upstream :protocol check | as above |
| no H3_DATAGRAM | webtransport-go Upgrade H3 datagram check (upstream) | as above; differs in SETTINGS_H3_DATAGRAM only |
| no QUIC datagrams | quic-go http3.Server: H3_DATAGRAM without max_datagram_frame_size is H3_SETTINGS_ERROR (upstream) | as above; asserts remote *quic.ApplicationError 0x109 |

For the four upstream rows the evidence is the valid baseline in the same
test: it gets 200, each case differs from it in that one field alone, and
each case's assertion names the specific refusal (body text or error code).

Note: the "version" a draft-16 client selects is the SETTINGS_WT_ENABLED
codepoint (section 7.1); the "no WT_ENABLED" case covers that, and the
"unsupported version" :protocol case is kept as before.
