// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * mtls-auth.spec.ts
 *
 * End-to-end mTLS *authentication* flow. The existing bootstrap-artifacts spec
 * only verifies that the admin client certificate exists on disk and is a valid
 * X.509 chained to the CA — it explicitly does NOT attach the cert for an
 * authenticated request. This spec closes that gap: it performs real mTLS
 * requests with Playwright's per-origin clientCertificates and asserts the
 * identity/authorization outcome.
 *
 * Daemon: auth-enabled=true (TLS listener defaults to verify-the-cert-if-given).
 * Management endpoints require an authenticated identity, so:
 *   - no client cert        -> 401 (unauthenticated)
 *   - admin client cert     -> 200, resolved as the bootstrapped admin (CN=admin)
 */

import { test, expect } from "./fixtures/auth-daemon.ts";
import { promises as fsp } from "fs";

test.describe("mTLS authentication", () => {
  test("management endpoint rejects an unauthenticated (no client cert) request with 401", async ({
    request,
    authDaemon,
  }) => {
    const resp = await request.get(`${authDaemon.adminUrl}/users/list`);
    expect(resp.status()).toBe(401);
  });

  test("admin client certificate authenticates and authorizes the request (200 + admin identity)", async ({
    playwright,
    authDaemon,
  }) => {
    // A dedicated request context that presents the bootstrapped admin mTLS
    // client certificate for the daemon's origin.
    const ctx = await playwright.request.newContext({
      ignoreHTTPSErrors: true,
      clientCertificates: [
        {
          origin: authDaemon.adminUrl,
          certPath: authDaemon.adminCertPath,
          keyPath: authDaemon.adminKeyPath,
        },
      ],
    });
    try {
      const resp = await ctx.get(`${authDaemon.adminUrl}/users/list`);
      expect(resp.status()).toBe(200);

      // Wire contract (pkg/api/rest/handlers_users.go): snake_case keys.
      const body = (await resp.json()) as {
        users: { username: string; tenant_id: string; lifecycle: string }[];
        total: number;
      };
      // The mTLS identity (CN=admin) is authorized to list users, and the
      // bootstrapped admin user is present in the system tenant.
      const admin = body.users.find((u) => u.username === "admin");
      expect(admin).toBeDefined();
      expect(admin?.tenant_id).toBe("system");
      expect(admin?.lifecycle).toBe("active");
    } finally {
      await ctx.dispose();
    }
  });

  test("the admin client certificate is CN=admin and chains to the daemon CA", async ({
    authDaemon,
  }) => {
    // Sanity-check the material actually used for the authenticated request
    // above: it must be the admin identity, signed by the auto-generated CA.
    const crypto = await import("crypto");
    const [adminPem, caPem] = await Promise.all([
      fsp.readFile(authDaemon.adminCertPath, "utf8"),
      fsp.readFile(authDaemon.caCertPath, "utf8"),
    ]);
    const adminCert = new crypto.X509Certificate(adminPem);
    const caCert = new crypto.X509Certificate(caPem);
    expect(adminCert.subject.toLowerCase()).toContain("cn=admin");
    expect(adminCert.verify(caCert.publicKey)).toBe(true);
  });
});
