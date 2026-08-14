// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Settings real-backend spec.
 *
 * Theme toggle persistence is purely client-side (localStorage) so it cannot
 * be tested against a live daemon at the API level. Instead this spec:
 *   1. Validates the /health endpoint round-trips as a pre-condition check.
 *   2. Documents that theme persistence is covered by the mocked GUI suite.
 *
 * Any server-side settings endpoints (none exist currently) would be added
 * here when they are introduced.
 */

import { test, expect } from "./fixtures/daemon.ts";
import { ApiClient } from "./fixtures/api.ts";

test.describe("Settings (real backend)", () => {
  test("daemon health is reachable — pre-condition for GUI settings", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const health = await api.get<{ status: string }>("/health");
    expect(health.status).toBe("ok");
  });

  test("GET /capabilities lists available features", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const caps = await api.get<Record<string, boolean>>("/capabilities");

    // kv must always be present
    expect(typeof caps["kv"]).toBe("boolean");
  });
});
