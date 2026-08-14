// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * kv-restore-mvcc-real.spec.ts drives an ACTUAL, end-to-end selective
 * point-in-time restore (POST /kv/restore) against a REAL raft.sm_type:
 * mvcc daemon -- fixtures/daemon.ts's `mvccDaemon` fixture, a second daemon
 * spawned independently of every other real-backend spec's shared `daemon`
 * (which deliberately stays on the default, non-MVCC state machine -- see
 * kv-restore-default-sm-real.spec.ts). This is the positive-path
 * counterpart: no mocking, no simulated conflicts, an actual Raft-backed
 * MVCC revision history read and written over the live REST API.
 *
 * Two scenarios, matching the two the CLI integration suite
 * (test/integration/kv_restore_cli_test.go) treats as the headline cases:
 *
 *   - revert: a key mutated after revision R comes back to its value AT R.
 *   - recreate-after-delete via --prefix: a key deleted after R is
 *     recreated by a PREFIX-scoped restore, which requires the durable
 *     revision index (a plain live-key range scan cannot see a deleted
 *     key at any revision) -- the real bulk-delete-recovery scenario this
 *     feature exists for.
 */

import { test, expect } from "./fixtures/daemon.ts";
import { ApiClient } from "./fixtures/api.ts";

function b64(text: string): string {
  return Buffer.from(text, "utf-8").toString("base64");
}

function uniqueKey(label: string): string {
  return `e2e-restore-mvcc-${label}-${Date.now()}-${Math.floor(Math.random() * 1e6)}`;
}

/** GET /kv/revision -- the current MVCC revision. */
async function currentRevision(api: ApiClient): Promise<number> {
  const resp = await api.get<{ revision: number }>("/kv/revision");
  return resp.revision;
}

interface RestoreKeyChange {
  key: string;
  action: "revert" | "recreate" | "delete";
  from_revision?: number;
  to_revision?: number;
  value_bytes?: number;
}

interface RestoreResult {
  revision: number;
  changes: RestoreKeyChange[];
  conflicts?: RestoreKeyChange[];
  applied: number;
  skipped: number;
  conflicted: number;
  dry_run: boolean;
}

test.describe("kv restore — real raft.sm_type: mvcc daemon, end to end", () => {
  test("revert: a key mutated after R comes back to its value at R, and the store moves forward (not a rewind)", async ({
    request,
    mvccDaemon,
  }) => {
    const api = new ApiClient(request, mvccDaemon.adminUrl);
    const k = uniqueKey("revert");

    await api.putText(`/kv/${encodeURIComponent(k)}`, "orig");
    const targetRevision = await currentRevision(api);
    const revisionBeforeMutate = targetRevision;

    await api.putText(`/kv/${encodeURIComponent(k)}`, "changed");
    const currentBeforeRestore = await currentRevision(api);
    expect(currentBeforeRestore).toBeGreaterThan(revisionBeforeMutate);

    // Confirm the mutation actually took effect before restoring, so the
    // restore's own effect below is unambiguous.
    const beforeRestore = await request.get(`${mvccDaemon.adminUrl}/kv/${encodeURIComponent(k)}`);
    expect(await beforeRestore.text()).toBe("changed");

    const result = await api.post<RestoreResult>("/kv/restore", {
      revision: targetRevision,
      keys: [b64(k)],
    });

    expect(result.applied).toBe(1);
    expect(result.conflicted).toBe(0);
    expect(result.changes).toHaveLength(1);
    expect(result.changes[0]?.action).toBe("revert");

    const afterRestore = await request.get(`${mvccDaemon.adminUrl}/kv/${encodeURIComponent(k)}`);
    expect(await afterRestore.text()).toBe("orig");

    // The restore is a forward write, never a Raft rewind: the store's
    // current revision after the restore must be strictly greater than it
    // was immediately before the restore call.
    const currentAfterRestore = await currentRevision(api);
    expect(currentAfterRestore).toBeGreaterThan(currentBeforeRestore);

    await api.delete(`/kv/${encodeURIComponent(k)}`);
  });

  test("recreate-after-delete via prefix: a bulk-deleted key is recovered by a prefix restore, sibling keys untouched", async ({
    request,
    mvccDaemon,
  }) => {
    const api = new ApiClient(request, mvccDaemon.adminUrl);
    const prefix = uniqueKey("recreate") + "/";
    const alpha = `${prefix}alpha`;
    const bravo = `${prefix}bravo`;

    await api.putText(`/kv/${encodeURIComponent(alpha)}`, "alpha-orig");
    await api.putText(`/kv/${encodeURIComponent(bravo)}`, "bravo-orig");
    const revisionBeforeDelete = await currentRevision(api);

    await api.delete(`/kv/${encodeURIComponent(alpha)}`);
    const missing = await request.get(`${mvccDaemon.adminUrl}/kv/${encodeURIComponent(alpha)}`);
    expect(missing.status()).toBe(404);

    const result = await api.post<RestoreResult>("/kv/restore", {
      revision: revisionBeforeDelete,
      prefix: b64(prefix),
    });

    expect(result.applied).toBe(1);
    expect(result.conflicted).toBe(0);
    expect(result.changes).toHaveLength(1);
    expect(result.changes[0]?.action).toBe("recreate");

    const recreated = await request.get(`${mvccDaemon.adminUrl}/kv/${encodeURIComponent(alpha)}`);
    expect(recreated.ok()).toBe(true);
    expect(await recreated.text()).toBe("alpha-orig");

    // bravo was never touched, so it must be exactly as it was.
    const untouched = await request.get(`${mvccDaemon.adminUrl}/kv/${encodeURIComponent(bravo)}`);
    expect(await untouched.text()).toBe("bravo-orig");

    await api.delete(`/kv/${encodeURIComponent(alpha)}`);
    await api.delete(`/kv/${encodeURIComponent(bravo)}`);
  });
});
