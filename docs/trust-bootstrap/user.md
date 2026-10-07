# Trust Bootstrap — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

How a client that has never spoken to a server gets a CA bundle it can actually
trust.

**Read this first:** the `qrdb bootstrap` verbs do not work yet. Every one of
them exits with a "pending backend" error — the implementation lives in
go-xkms, and qrdb carries the command shape so the surface matches across the
stack's tools. Everything below about *obtaining* a bundle by hand does work.

## The problem it solves

You need the server's CA certificate before you can verify the server's TLS. If
you just download it over the connection you have not verified, anything that
can answer can hand you its own root and you cannot tell. Each mode gets one
trusted fact to you by a different route:

| Mode | Where the trusted fact comes from |
|---|---|
| `dane` | a DNSSEC-signed TLSA record |
| `noise` | a Noise XK handshake using a static key you already have |
| `spki` | a SHA-256 hash somebody handed you out of band |
| `direct` | the CA bundle itself, copied by an operator |
| `auto` | whichever of the above is available, strongest first |

## Getting a bundle today

**Copy it out of band.** The most direct and, done properly, entirely sound
approach: get `ca.pem` from the server's data directory by a channel you already
trust — configuration management, a signed artifact, a USB stick.

**Or fetch it from the bootstrap endpoint, and verify it independently.**

```bash
curl -k https://db.example.com/v1/ca/bootstrap -o ca-bootstrap.pem
sha256sum ca-bootstrap.pem       # compare against a fingerprint you were given
```

That endpoint only exists if the deployment configured a bootstrap certificate;
otherwise you get a 404. And note the `-k`: you are downloading over an
unverified connection, so the fingerprint comparison **is** the security. Skip
it and you have trust-on-first-use with no verification.

## Using the bundle

```bash
qrdb --server https://db.example.com \
     --ca-file ca-bootstrap.pem \
     --cert-file client.pem --key-file client-key.pem \
     barrier status
```

## Checking a server before you have credentials

```bash
curl -k https://db.example.com/v1/config/initialized
```

This is public on purpose — it is the one question a client can ask before it
has anything.

## Troubleshooting

**`qrdb bootstrap auto` (or any mode) fails immediately.** Expected. The
command shape exists so it matches the other tools in the stack; the backend
that implements it is in go-xkms. Obtain the bundle by hand as above.

**`/v1/ca/bootstrap` returns 404.** The deployment has not configured a
bootstrap certificate, so the route is not registered at all. Get `ca.pem` out
of band instead.

**You are not sure whether the bundle you downloaded is the right one.** Then
you do not have trust yet. Compare its SHA-256 against a value that reached you
by a different channel, or copy the file directly from a source you already
trust. Downloading it over `-k` and using it unverified defeats the exercise.

**You confused this with `qrdb init`.** `init` is the *server* being set up for
the first time. This is a *client* establishing trust to a server that already
exists.

**You confused this with cluster discovery.** Finding Raft peers is
[../clustering/user.md](../clustering/user.md). Trust bootstrap is about CA
trust, not membership.
