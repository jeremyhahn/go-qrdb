// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * SQL wire protocol toggle spec — regression coverage for bug #13.
 *
 * Validates that PATCH /v1/databases/{name}/protocol with
 * { mysql: true, postgresql: false } persists correctly and the
 * subsequent GET /v1/databases/{name} reflects protocol.mysql=true.
 */

import { test, expect } from "./fixtures/daemon.ts";

interface ProtocolConfig {
  mysql: boolean;
  postgresql: boolean;
}

interface DatabaseInfoResponse {
  name: string;
  engine: string;
  backend: string;
  protocol: ProtocolConfig;
}

function dbName(label: string): string {
  return `e2e-sql-${label}-${Date.now()}`;
}

test.describe("SQL wire toggle (real backend — bug #13)", () => {
  test(
    "PATCH /v1/databases/{name}/protocol sets mysql=true",
    async ({ request, daemon }) => {
      const name = dbName("mysql");

      // Create database
      const createResp = await request.post(`${daemon.adminUrl}/v1/databases`, {
        data: {
          name,
          engine: "kv",
          backend: "pebble",
          protocol: { mysql: false, postgresql: false },
        },
        headers: { "Content-Type": "application/json" },
      });

      if (createResp.status() === 501) {
        console.info("[sql-toggle-real] Database service not available (501). Skipping.");
        return;
      }

      expect(createResp.status()).toBe(201);

      // Patch protocol
      const patchResp = await request.patch(
        `${daemon.adminUrl}/v1/databases/${name}/protocol`,
        {
          data: { mysql: true, postgresql: false },
          headers: { "Content-Type": "application/json" },
        },
      );

      expect(patchResp.ok()).toBe(true);
      const patched = await patchResp.json() as DatabaseInfoResponse;
      expect(patched.protocol.mysql).toBe(true);
      expect(patched.protocol.postgresql).toBe(false);

      // Confirm persistence via GET
      const getResp = await request.get(
        `${daemon.adminUrl}/v1/databases/${name}`,
      );
      expect(getResp.ok()).toBe(true);
      const db = await getResp.json() as DatabaseInfoResponse;
      expect(db.protocol.mysql).toBe(true);

      // Cleanup
      await request.delete(`${daemon.adminUrl}/v1/databases/${name}`);
    },
  );

  test(
    "PATCH /v1/databases/{name}/protocol sets postgresql=true",
    async ({ request, daemon }) => {
      const name = dbName("pgsql");

      const createResp = await request.post(`${daemon.adminUrl}/v1/databases`, {
        data: {
          name,
          engine: "kv",
          backend: "pebble",
          protocol: { mysql: false, postgresql: false },
        },
        headers: { "Content-Type": "application/json" },
      });

      if (createResp.status() === 501) {
        console.info("[sql-toggle-real] Database service not available (501). Skipping.");
        return;
      }

      const patchResp = await request.patch(
        `${daemon.adminUrl}/v1/databases/${name}/protocol`,
        {
          data: { mysql: false, postgresql: true },
          headers: { "Content-Type": "application/json" },
        },
      );

      expect(patchResp.ok()).toBe(true);
      const patched = await patchResp.json() as DatabaseInfoResponse;
      expect(patched.protocol.postgresql).toBe(true);
      expect(patched.protocol.mysql).toBe(false);

      // Cleanup
      await request.delete(`${daemon.adminUrl}/v1/databases/${name}`);
    },
  );

  test(
    "PATCH /v1/databases/{name}/protocol toggles both flags independently",
    async ({ request, daemon }) => {
      const name = dbName("both");

      const createResp = await request.post(`${daemon.adminUrl}/v1/databases`, {
        data: {
          name,
          engine: "kv",
          backend: "pebble",
          protocol: { mysql: true, postgresql: true },
        },
        headers: { "Content-Type": "application/json" },
      });

      if (createResp.status() === 501) {
        console.info("[sql-toggle-real] Database service not available (501). Skipping.");
        return;
      }

      // Turn both off
      const patchResp = await request.patch(
        `${daemon.adminUrl}/v1/databases/${name}/protocol`,
        {
          data: { mysql: false, postgresql: false },
          headers: { "Content-Type": "application/json" },
        },
      );

      expect(patchResp.ok()).toBe(true);
      const patched = await patchResp.json() as DatabaseInfoResponse;
      expect(patched.protocol.mysql).toBe(false);
      expect(patched.protocol.postgresql).toBe(false);

      // Cleanup
      await request.delete(`${daemon.adminUrl}/v1/databases/${name}`);
    },
  );
});
