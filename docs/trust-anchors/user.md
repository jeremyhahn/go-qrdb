# Trust Anchors — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

A trust anchor is a CA certificate your tenant registers so that certificates
it issued can be enrolled on your tenant's accounts and log in as them. Use
one when your organisation already runs its own PKI and you want your users'
existing certificates to work, instead of having qrdb's per-tenant CA sign
new ones from CSRs.

## What an anchor does, and does not, do

- It vouches **only for your tenant**. A certificate from your CA that names
  another tenant is refused, both at enrollment and at login.
- It does not create accounts. An account must exist before a certificate is
  enrolled on it.
- It does not by itself let anyone log in. A certificate logs in only once it
  is enrolled (bound) to an account, and under the `required` binding policy
  that binding is what makes the login work at all.

## Typical flow

1. Register your issuing CA (or your root, if your users' certificates carry
   their intermediates):

   ```sh
   qrdb tenant trust-anchors add --tenant-id acme --file acme-issuing-ca.pem
   ```

2. Create the account, then enroll the user's certificate on it:

   ```sh
   qrdb ca enroll --username bob --cert bob.pem --chain intermediates.pem
   ```

   The certificate's subject must name the account (CN `bob`) and your tenant,
   it must permit client authentication, and it must chain to one of your
   anchors.

3. Bob connects to `db.example.com` with that certificate and key.

A new tenant can do all of this at creation time: the operator passes your
first admin's certificate with `--admin-cert` and your CA with
`--trust-anchors`.

## Removing an anchor

```sh
qrdb tenant trust-anchors remove --tenant-id acme --fingerprint <fingerprint>
```

Logins by certificates that only that anchor vouched for stop at once: every
login checks your tenant's anchors fresh. The bindings stay on the accounts; remove them with the identity commands if the
certificates are retired for good.

## Limits

- At most 32 anchors per tenant.
- Each anchor must be a CA certificate inside its validity window, with a
  strong key and signature algorithm ([api.md](api.md#validation)). An expired
  anchor cannot be registered; one that expires after registration stops
  vouching when it expires.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `enroll` reports the tenant has no trust anchors | Register one first |
| `enroll` reports the certificate is not trusted | It does not chain to any of your anchors; pass its intermediates with `--chain` |
| `enroll` reports an identity mismatch | The certificate's CN or tenant is not the account you named |
| Permission denied | You named another tenant; anchors are managed by each tenant's own admin |
