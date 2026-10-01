// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Transaction real-backend spec — regression coverage for bug #8.
 *
 * Validates that POST /tx serializes compare preconditions and success/failure
 * operations with the correct string enums the server expects:
 *   target: "value" | "version" | "create_revision" | "mod_revision"
 *   result: "equal" | "greater" | "less" | "not_equal"
 *   op type: "put" | "get" | "delete" | "range"
 *
 * Before the fix these were sent as numeric enum values that the server
 * silently rejected by falling back to defaults, causing the wrong branch
 * to execute.
 */

import { test, expect } from "./fixtures/daemon.ts";
import { ApiClient } from "./fixtures/api.ts";
import type { TransactionResponse } from "./fixtures/api.ts";

function key(label: string): string {
  return `e2e-tx-${label}-${Date.now()}`;
}

test.describe("Transaction (real backend — bug #8)", () => {
  test(
    "compare target=value, result=equal: condition met → success ops execute",
    async ({ request, daemon }) => {
      const api = new ApiClient(request, daemon.adminUrl);
      const k = key("cond-met");

      // Seed the key with a known value
      await api.putText(`/kv/${encodeURIComponent(k)}`, "expected-value");

      const tx = await api.post<TransactionResponse>("/tx", {
        compares: [
          {
            key: k,
            target: "value",
            result: "equal",
            value: "expected-value",
          },
        ],
        success_ops: [{ type: "put", key: k, value: "success-written" }],
        failure_ops: [{ type: "put", key: k, value: "failure-written" }],
      });

      expect(tx.succeeded).toBe(true);

      // Verify the success op ran
      const resp = await request.get(
        `${daemon.adminUrl}/kv/${encodeURIComponent(k)}`,
      );
      expect(await resp.text()).toBe("success-written");

      // Cleanup
      await api.delete(`/kv/${encodeURIComponent(k)}`);
    },
  );

  test(
    "compare target=value, result=equal: condition NOT met → failure ops execute",
    async ({ request, daemon }) => {
      const api = new ApiClient(request, daemon.adminUrl);
      const k = key("cond-not-met");

      // Seed the key with a value that does NOT match the compare
      await api.putText(`/kv/${encodeURIComponent(k)}`, "actual-value");

      const tx = await api.post<TransactionResponse>("/tx", {
        compares: [
          {
            key: k,
            target: "value",
            result: "equal",
            value: "wrong-expectation",
          },
        ],
        success_ops: [{ type: "put", key: k, value: "success-written" }],
        failure_ops: [{ type: "put", key: k, value: "failure-written" }],
      });

      expect(tx.succeeded).toBe(false);

      // Verify the failure op ran
      const resp = await request.get(
        `${daemon.adminUrl}/kv/${encodeURIComponent(k)}`,
      );
      expect(await resp.text()).toBe("failure-written");

      // Cleanup
      await api.delete(`/kv/${encodeURIComponent(k)}`);
    },
  );

  test(
    "compare target=version, result=greater: version > 0 when key exists",
    async ({ request, daemon }) => {
      const api = new ApiClient(request, daemon.adminUrl);
      const k = key("version-gt");

      await api.putText(`/kv/${encodeURIComponent(k)}`, "v1");

      // version > 0 should be true for any existing key
      const tx = await api.post<TransactionResponse>("/tx", {
        compares: [{ key: k, target: "version", result: "greater", version: 0 }],
        success_ops: [{ type: "put", key: k, value: "version-ok" }],
        failure_ops: [],
      });

      expect(tx.succeeded).toBe(true);

      // Cleanup
      await api.delete(`/kv/${encodeURIComponent(k)}`);
    },
  );

  test(
    "empty compares with single put op succeeds unconditionally",
    async ({ request, daemon }) => {
      const api = new ApiClient(request, daemon.adminUrl);
      const k = key("no-compare");

      const tx = await api.post<TransactionResponse>("/tx", {
        compares: [],
        success_ops: [{ type: "put", key: k, value: "unconditional" }],
        failure_ops: [],
      });

      expect(tx.succeeded).toBe(true);

      // Verify the key was written
      const resp = await request.get(
        `${daemon.adminUrl}/kv/${encodeURIComponent(k)}`,
      );
      expect(await resp.text()).toBe("unconditional");

      // Cleanup
      await api.delete(`/kv/${encodeURIComponent(k)}`);
    },
  );

  test(
    "a get op in a branch is refused (400) and the key is untouched",
    async ({ request, daemon }) => {
      // A transaction branch only writes: get and range ops are refused before
      // anything is proposed, rather than defaulting to a write that would
      // overwrite the key (docs/transactions/api.md).
      const api = new ApiClient(request, daemon.adminUrl);
      const k = key("get-op");

      await api.putText(`/kv/${encodeURIComponent(k)}`, "get-me");

      const resp = await request.post(`${daemon.adminUrl}/tx`, {
        data: { compares: [], success_ops: [{ type: "get", key: k }], failure_ops: [] },
        headers: { "Content-Type": "application/json" },
      });
      expect(resp.status()).toBe(400);
      expect(await resp.text()).toContain("transaction branches support only Put and Delete");

      const after = await request.get(`${daemon.adminUrl}/kv/${encodeURIComponent(k)}`);
      expect(await after.text()).toBe("get-me");

      // Cleanup
      await api.delete(`/kv/${encodeURIComponent(k)}`);
    },
  );
});
