# Reference standards

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-29
> Conflicts with this document are defects. Fix them here first. Every other
> file is a standard's text, copied unmodified. A Markdown copy carries a
> REFERENCE banner and `Last verified:` stamp under its title, naming its
> source and revision; the text below the banner is the publisher's. A `.txt`
> copy carries none.

The industry standards go-qrdb implements, one entry per document. Where a
standard exists the code implements it precisely and designs nothing in its
place; a draft is adopted at one pinned revision, and `ROADMAP.md` carries a
standards-watch line for it under Dependencies.

Each copy is the publisher's text, unmodified. A row whose file is "link only"
has no copy in this directory, for the reason given under the table.

## Index

| File | Standard | Revision | Implementing package(s) | Publisher URL |
|---|---|---|---|---|
| [rfc1123.txt](rfc1123.txt) | Requirements for Internet Hosts -- Application and Support (DNS label syntax) | RFC 1123 | `pkg/vector` (collection names) | https://www.rfc-editor.org/rfc/rfc1123.txt |
| [rfc3339.txt](rfc3339.txt) | Date and Time on the Internet: Timestamps | RFC 3339 | timestamp encoding across `pkg/api`, `sdk/go`, `cmd/qrdb` | https://www.rfc-editor.org/rfc/rfc3339.txt |
| [rfc4648.txt](rfc4648.txt) | The Base16, Base32, and Base64 Data Encodings | RFC 4648 | `pkg/api/mcp`, `pkg/api/rest`, `sdk/go/transport` (binary values in JSON) | https://www.rfc-editor.org/rfc/rfc4648.txt |
| [rfc5705.txt](rfc5705.txt) | Keying Material Exporters for Transport Layer Security (TLS) | RFC 5705 | `pkg/initproof` | https://www.rfc-editor.org/rfc/rfc5705.txt |
| [rfc6698.txt](rfc6698.txt) | The DNS-Based Authentication of Named Entities (DANE) Transport Layer Security (TLS) Protocol: TLSA | RFC 6698 | `cmd/qrdb` (bootstrap `dane` pinning mode) | https://www.rfc-editor.org/rfc/rfc6698.txt |
| [rfc7292.txt](rfc7292.txt) | PKCS #12: Personal Information Exchange Syntax v1.1 | RFC 7292 | `pkg/cert` | https://www.rfc-editor.org/rfc/rfc7292.txt |
| [rfc8259.txt](rfc8259.txt) | The JavaScript Object Notation (JSON) Data Interchange Format | RFC 8259 | `pkg/auth/abac` (quoted condition values, section 7) | https://www.rfc-editor.org/rfc/rfc8259.txt |
| [rfc8446.txt](rfc8446.txt) | The Transport Layer Security (TLS) Protocol Version 1.3 | RFC 8446 | `pkg/initproof` (exporter, section 7.5); every TLS listener | https://www.rfc-editor.org/rfc/rfc8446.txt |
| [rfc8555.txt](rfc8555.txt) | Automatic Certificate Management Environment (ACME) | RFC 8555 | `pkg/permission`, `sdk/go` (ACME administration scope names only; the ACME server lives in go-xkms) | https://www.rfc-editor.org/rfc/rfc8555.txt |
| [rfc9000.txt](rfc9000.txt) | QUIC: A UDP-Based Multiplexed and Secure Transport | RFC 9000 | `pkg/api/quic`, `sdk/go/transport/quic`, `pkg/api/rest` (via quic-go) | https://www.rfc-editor.org/rfc/rfc9000.txt |
| [rfc9106.txt](rfc9106.txt) | Argon2 Memory-Hard Function for Password Hashing and Proof-of-Work Applications | RFC 9106 | `pkg/seal/kdf`, `pkg/pin`, `pkg/auth` (Argon2id) | https://www.rfc-editor.org/rfc/rfc9106.txt |
| link only | HTTP Semantics | RFC 9110 | `pkg/api/rest`, `pkg/auth/middleware`, `sdk/go/transport/rest` (Allow, Retry-After) | https://www.rfc-editor.org/rfc/rfc9110.txt |
| [rfc9113.txt](rfc9113.txt) | HTTP/2 | RFC 9113 | `pkg/api/rest` | https://www.rfc-editor.org/rfc/rfc9113.txt |
| [rfc9114.txt](rfc9114.txt) | HTTP/3 | RFC 9114 | `pkg/api/rest` (via quic-go) | https://www.rfc-editor.org/rfc/rfc9114.txt |
| [rfc9162.txt](rfc9162.txt) | Certificate Transparency Version 2.0 | RFC 9162 | `pkg/vlog` (Merkle tree hashing and proofs, section 2.1; signed heads in Ed25519 or ECDSA P-256 with SHA-256, sections 2.2 and 10.2.2) | https://www.rfc-editor.org/rfc/rfc9162.txt |
| [rfc9180.txt](rfc9180.txt) | Hybrid Public Key Encryption | RFC 9180 | `pkg/keydelivery` | https://www.rfc-editor.org/rfc/rfc9180.txt |
| [rfc9449.txt](rfc9449.txt) | OAuth 2.0 Demonstrating Proof of Possession (DPoP) | RFC 9449 | `pkg/auth`, `pkg/auth/middleware`, `sdk/go` | https://www.rfc-editor.org/rfc/rfc9449.txt |
| [rfc9562.txt](rfc9562.txt) | Universally Unique IDentifiers (UUIDs) | RFC 9562 | `pkg/role` (UUIDv8 over SHA-256, section 5.8) | https://www.rfc-editor.org/rfc/rfc9562.txt |
| [rfc9728.txt](rfc9728.txt) | OAuth 2.0 Protected Resource Metadata | RFC 9728 | `pkg/api/mcp`, `pkg/server` | https://www.rfc-editor.org/rfc/rfc9728.txt |
| [draft-ietf-hpke-pq-05.txt](draft-ietf-hpke-pq-05.txt) | Post-Quantum and Post-Quantum/Traditional Hybrid Algorithms for HPKE | draft -05 (see the note on the pin below) | `pkg/keydelivery` (MLKEM768-X25519, "X-Wing", through Go's `crypto/hpke`) | https://datatracker.ietf.org/doc/draft-ietf-hpke-pq/05/ |
| link only | Recommendation for Block Cipher Modes of Operation: Galois/Counter Mode (GCM) and GMAC | NIST SP 800-38D (2007) | `pkg/crypto/aesgcm`, `pkg/seal`, `pkg/seal/encfs`, `pkg/keywrap`, `pkg/service` | https://nvlpubs.nist.gov/nistpubs/Legacy/SP/nistspecialpublication800-38d.pdf |
| link only | Recommendation for Password-Based Key Derivation, Part 1: Storage Applications | NIST SP 800-132 (2010) | `pkg/seal/kdf`, `pkg/seal`, `pkg/auth`, `pkg/pin` (PBKDF2-HMAC-SHA256) | https://nvlpubs.nist.gov/nistpubs/Legacy/SP/nistspecialpublication800-132.pdf |
| link only | Recommendation for Pair-Wise Key-Establishment Schemes Using Discrete Logarithm Cryptography | NIST SP 800-56A Rev. 3 | `pkg/clusterenroll` | https://nvlpubs.nist.gov/nistpubs/SpecialPublications/NIST.SP.800-56Ar3.pdf |
| link only | Recommendation for Applications Using Approved Hash Algorithms | NIST SP 800-107 Rev. 1 | `pkg/discovery` (HMAC key floor, section 5.3.4) | https://nvlpubs.nist.gov/nistpubs/Legacy/SP/nistspecialpublication800-107r1.pdf |
| link only | Interfaces for Personal Identity Verification | NIST SP 800-73-4 | `sdk/go/authfactor`, `pkg/auth` (PIV factor name only; the PIV implementation lives in go-xkms) | https://nvlpubs.nist.gov/nistpubs/SpecialPublications/NIST.SP.800-73-4.pdf |
| link only | Security Requirements for Cryptographic Modules | FIPS 140-3 | `pkg/clusterenroll`, `pkg/keydelivery`, `pkg/seal/kdf`, `pkg/pin`, `pkg/auth`, `cmd/qrdbd` (FIPS mode, `GODEBUG=fips140=only`) | https://nvlpubs.nist.gov/nistpubs/FIPS/NIST.FIPS.140-3.pdf |
| [spiffe-id.md](spiffe-id.md) | The SPIFFE Identity and Verifiable Identity Document (SPIFFE ID) | spiffe/spiffe commit `665a28f3253ecdce59f26f3ef29c0d6408b37c3d` | `pkg/auth/spiffe`, `pkg/cert`, `pkg/config` | https://github.com/spiffe/spiffe/blob/665a28f3253ecdce59f26f3ef29c0d6408b37c3d/standards/SPIFFE-ID.md |
| [c2sp-signed-note.md](c2sp-signed-note.md) | Note (signed-note): cleartext signed messages | C2SP/C2SP commit `fea9004f73fbec8cf7a8b1936430e2487eb23b55` | `pkg/vlog` (the checkpoint's note format and signature line; key IDs for signature types 0x01 Ed25519 and 0x02 ECDSA) | https://github.com/C2SP/C2SP/blob/fea9004f73fbec8cf7a8b1936430e2487eb23b55/signed-note.md |
| [c2sp-tlog-checkpoint.md](c2sp-tlog-checkpoint.md) | Transparency Log Checkpoints (tlog-checkpoint) | C2SP/C2SP commit `fea9004f73fbec8cf7a8b1936430e2487eb23b55` | `pkg/vlog` (the checkpoint's note text: origin, tree size and root hash; Ed25519 log signatures) | https://github.com/C2SP/C2SP/blob/fea9004f73fbec8cf7a8b1936430e2487eb23b55/tlog-checkpoint.md |
| [draft-ietf-webtrans-http3-16.txt](draft-ietf-webtrans-http3-16.txt) | WebTransport over HTTP/3 | draft -16 (see the deviations below) | `pkg/api/rest` (sessions on the HTTP/3 listener, through `internal/webtransport`, patched `quic-go/webtransport-go` v0.13.0) | https://datatracker.ietf.org/doc/draft-ietf-webtrans-http3/16/ |
| [grpc-http2-protocol.md](grpc-http2-protocol.md) | gRPC over HTTP2 | grpc/grpc commit `5563edae1431e2955c38b1f1ba1ccf7f2c4cebd2` | `pkg/api/grpcweb` (gRPC-Web framing over the gRPC server's own handler, served by `pkg/api/rest`) | https://github.com/grpc/grpc/blob/5563edae1431e2955c38b1f1ba1ccf7f2c4cebd2/doc/PROTOCOL-HTTP2.md |
| [grpc-web-protocol.md](grpc-web-protocol.md) | gRPC Web | grpc/grpc commit `5563edae1431e2955c38b1f1ba1ccf7f2c4cebd2` | `pkg/api/grpcweb` (gRPC-Web framing over the gRPC server's own handler, served by `pkg/api/rest`) | https://github.com/grpc/grpc/blob/5563edae1431e2955c38b1f1ba1ccf7f2c4cebd2/doc/PROTOCOL-WEB.md |
| link only | MySQL Client/Server Protocol | as implemented by go-mysql-server | `pkg/sql/mysql` (via dolthub go-mysql-server and vitess) | https://dev.mysql.com/doc/dev/mysql-server/latest/PAGE_PROTOCOL.html |
| link only | PostgreSQL Frontend/Backend Protocol | protocol 3.0 | `pkg/sql/pgsql` (via jackc/pgx) | https://www.postgresql.org/docs/current/protocol.html |
| link only | etcd v3 API | etcd API v3.7 (`go.etcd.io/etcd/api/v3` v3.7.2) | `pkg/api/etcd` | https://etcd.io/docs/v3.7/learning/api/ |
| link only | Model Context Protocol specification | as implemented by `mark3labs/mcp-go` v1.3.0 | `pkg/api/mcp`, `sdk/go/transport/mcp` | https://modelcontextprotocol.io/specification |
| link only | In Search of an Understandable Consensus Algorithm (Raft) | extended version, 2014 | go-quicraft (consensus; consumed, not implemented, here) | https://raft.github.io/raft.pdf |

## Why some rows are link only

- **NIST publications and RFC 9110.** These may be copied (NIST publications
  are not subject to copyright; IETF documents carry the IETF Trust's
  licence), and a copy is owed. The repository's publication-bound scanner
  reads every file under `docs/*/` with no exemption list, so the binary bytes
  of a PDF, and one phrase of RFC 9110's own prose, read to it as violations.
  The copies land once the scanner can tell a verbatim third-party standard
  from project prose.
- **MySQL, PostgreSQL, etcd, MCP and the Raft paper.** Their redistribution
  terms are vendor documentation or paper copyright that this index does not
  confirm, so they are linked, not copied.

## The draft-ietf-hpke-pq pin

`pkg/keydelivery` reaches this draft only through Go's standard library
`crypto/hpke` (`MLKEM768X25519`), whose documentation names the draft and not a
revision; neither the qrdb code nor its tests name one either. The X-Wing
codepoint Go's test vectors use, `0x647a`, first appears in -02 and is
unchanged through -05. The copy here is -05; which revision the Go toolchain
in `go.mod` implements is unconfirmed, so the pin is provisional until that is
checked against the toolchain's source.

## The draft-ietf-webtrans-http3 pin and its deviations

`pkg/api/rest` serves WebTransport sessions as draft -16 specifies, through
`internal/webtransport` (a patched copy of `github.com/quic-go/webtransport-go`
v0.13.0), and only when a handler is registered (`WithWebTransportHandler`).
With a handler, the listener sends SETTINGS_WT_ENABLED (`0x2c7cf000`, section
9.2), SETTINGS_WT_INITIAL_MAX_STREAMS_BIDI/_UNI and
SETTINGS_WT_INITIAL_MAX_DATA (section 5) and SETTINGS_H3_DATAGRAM, and
its QUIC transport parameters enable datagrams and reliable stream resets
(section 3). With no handler it sends none of these, and the path is an
unknown route. In two places the library has no switch, and the behaviour
differs from -16:

- **Legacy SETTINGS.** With a handler, webtransport-go also sends the draft -06
  setting `0x2b603742` and SETTINGS_WT_MAX_SESSIONS. Draft -16 no longer
  defines either; they let clients of earlier revisions connect. A -16 client
  ignores both, as HTTP/3 requires for unknown settings (RFC 9114, section
  7.2.4.1).
- **SETTINGS_ENABLE_CONNECT_PROTOCOL.** quic-go's HTTP/3 server sends it
  (RFC 9220) on every connection, with or without a handler. It enables
  extended CONNECT, not WebTransport: without a handler, a CONNECT to any path
  is refused as an unknown route.
