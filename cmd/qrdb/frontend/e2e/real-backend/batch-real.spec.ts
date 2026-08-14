// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Batch real-backend spec — regression coverage for bug #8 (batch half).
 *
 * Validates that POST /batch with string enum op types ("put", "delete")
 * executes correctly. Before the fix the SDK sent numeric enum values,
 * causing the server to silently ignore unknown types.
 */

import { test, expect } from "./fixtures/daemon.ts";
import { ApiClient } from "./fixtures/api.ts";
import type { BatchResponse } from "./fixtures/api.ts";

function key(label: string): string {
  return `e2e-batch-${label}-${Date.now()}`;
}

test.describe("Batch operations (real backend — bug #8)", () => {
  test("batch PUT+DELETE: written key visible, deleted key gone", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const kPut = key("put");
    const kDel = key("del");

    // Pre-seed the key to delete
    await api.putText(`/kv/${encodeURIComponent(kDel)}`, "to-be-deleted");

    const batch = await api.post<BatchResponse>("/batch", {
      ops: [
        { type: "put", key: kPut, value: "batch-written" },
        { type: "delete", key: kDel },
      ],
    });

    expect(Array.isArray(batch.responses)).toBe(true);

    // Verify PUT took effect
    const putResp = await request.get(
      `${daemon.adminUrl}/kv/${encodeURIComponent(kPut)}`,
    );
    expect(putResp.ok()).toBe(true);
    expect(await putResp.text()).toBe("batch-written");

    // Verify DELETE took effect
    const delResp = await request.get(
      `${daemon.adminUrl}/kv/${encodeURIComponent(kDel)}`,
    );
    expect(delResp.status()).toBe(404);

    // Cleanup
    await api.delete(`/kv/${encodeURIComponent(kPut)}`);
  });

  test("batch with multiple PUTs writes all keys", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const keys = [key("a"), key("b"), key("c")];

    const ops = keys.map((k, i) => ({ type: "put", key: k, value: `val-${i}` }));

    const batch = await api.post<BatchResponse>("/batch", { ops });
    expect(Array.isArray(batch.responses)).toBe(true);

    // Verify all keys were written
    for (let i = 0; i < keys.length; i++) {
      const resp = await request.get(
        `${daemon.adminUrl}/kv/${encodeURIComponent(keys[i]!)}`,
      );
      expect(resp.ok()).toBe(true);
      expect(await resp.text()).toBe(`val-${i}`);
    }

    // Cleanup
    for (const k of keys) {
      await api.delete(`/kv/${encodeURIComponent(k)}`);
    }
  });

  test("empty batch returns empty responses array", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const batch = await api.post<BatchResponse>("/batch", { ops: [] });
    expect(Array.isArray(batch.responses)).toBe(true);
    expect(batch.responses.length).toBe(0);
  });
});
