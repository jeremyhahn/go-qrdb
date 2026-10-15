# Trust Anchors — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

`qrdb tenant trust-anchors` manages your own tenant's trust anchors. Every
verb names the tenant with `--tenant-id`, and it must be yours: the
permissions are tenant-self only (see [api.md](api.md)).

## add

```sh
qrdb tenant trust-anchors add --tenant-id acme --file acme-issuing-ca.pem
```

| Flag | Required | Meaning |
|---|---|---|
| `--tenant-id` | yes | Your tenant |
| `--file` | yes | A PEM bundle of one or more CA certificates |

Prints the bundle's anchors as registered. Every certificate must be a CA
certificate inside its validity window that permits certificate signing; one
bad certificate refuses the whole bundle. A tenant holds at most 32 anchors.

## list

```sh
qrdb tenant trust-anchors list --tenant-id acme
```

```
FINGERPRINT                                                       SUBJECT                    NOT AFTER
9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08  CN=Acme Issuing CA,O=Acme  2031-01-01
```

`-o json` and `-o yaml` print every field, including each anchor's PEM.

## remove

```sh
qrdb tenant trust-anchors remove --tenant-id acme \
  --fingerprint 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
```

The anchor stops vouching for logins at once: every login checks the tenant's
anchors fresh, and the change also schedules an immediate rebuild of the
node's client trust pool. Certificates already enrolled under the
removed anchor no longer log in unless another registered anchor still
vouches for them.

## Registering anchors at tenant create

An operator creating a tenant whose first admin brings its own certificate
passes the tenant's CA in the same command:

```sh
qrdb tenant create --id globex --name "Globex" \
  --admin-username gina --admin-cert gina.pem \
  --trust-anchors globex-issuing-ca.pem
```

See [multi-tenancy/cli.md](../multi-tenancy/cli.md).

## Enrolling a certificate under an anchor

```sh
qrdb ca enroll --username bob --cert bob.pem [--chain intermediates.pem]
```

See [certificate-authority/cli.md](../certificate-authority/cli.md).
