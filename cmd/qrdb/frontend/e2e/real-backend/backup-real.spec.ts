// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

/**
 * Backup manager real-backend spec.
 *
 * Exercises POST /v1/backups, GET /v1/backups/list, GET /v1/backups/{id},
 * POST /v1/backups/{id}/verify, POST /v1/backups/restore, and
 * DELETE /v1/backups/{id} against a LIVE qrdbd daemon (fixtures/daemon.ts
 * spawns a single-node Raft host via `--discovery static`). Every
 * assertion reads the real backend response: shard snapshots are captured
 * through the host's actual Raft SyncRequestSnapshot/ExportSnapshot path
 * (see pkg/service/shard_snapshot_provider.go), so a non-zero `size` and a
 * real sha256 `checksum` in the response prove the backend produced an
 * actual backup artifact — nothing here is mocked (no page.route /
 * wails-mock, unlike the fast tier's e2e/backup-manager.spec.ts).
 *
 * ONLINE RESTORE: POST /v1/backups/restore now performs a SAFETY-GUARDED
 * online restore (pkg/service/shard_snapshot_provider.go RestoreShardOnline)
 * instead of unconditionally hitting *quicraft.Host.ImportSnapshot's
 * running-shard refusal. It is only SAFE/EFFECTIVE for a non-system shard
 * where this node holds the sole voting replica (RF=1): stopping the
 * shard, importing, and restarting it in place would take down the system
 * tenant's auth data plane — including the very admin call that would
 * restart it — if attempted on the system shard, so that shard is refused
 * unconditionally in favor of the OFFLINE `qrdbd start --restore-backup-id`
 * boot flow (cmd/qrdbd/restore.go). This daemon fixture spawns a
 * single-node cluster whose SOLE shard IS the system shard, so the
 * "restores against a LIVE shard" test below pins exactly that guarded
 * refusal: the request succeeds at the transport level (200 OK), reports
 * zero shards restored, carries a per-shard error identifying the system
 * shard and pointing at the offline flow, and — critically — leaves the
 * live data UNCHANGED. The guard is unit/integration-tested directly for a
 * non-system RF=1 dedicated shard in pkg/server/raft_backend_factory_test.go
 * (TestRaftBackendFactory_OnlineBackupRestore_RF1Success), which a
 * single-node fixture like this one cannot exercise since it has no
 * dedicated per-tenant Raft shard to restore online.
 */

import { test, expect } from "./fixtures/daemon.ts";
import { ApiClient } from "./fixtures/api.ts";

interface BackupResponse {
  id: string;
  timestamp: string;
  size: number;
  shard_count: number;
  status: string;
  checksum: string;
  destination: string;
  compression: string;
  encryption: string;
  duration_ms: number;
  error_message?: string;
}

interface ListBackupsResponse {
  backups: BackupResponse[];
  total: number;
}

interface RestoreResultResponse {
  backup_id: string;
  shards_restored: number;
  duration_ms: number;
  errors?: { shard_id: number; message: string }[];
}

interface VerifyResultResponse {
  valid: boolean;
  checksum_match: boolean;
  expected_checksum: string;
  actual_checksum: string;
  errors?: string[];
}

function key(label: string): string {
  return `e2e-backup-${label}-${Date.now()}`;
}

/**
 * A backup "destination" label — a real filesystem path the daemon host
 * could write to, matching how the CLI/GUI populate the field. The test
 * daemon runs without --backup-dir (in-memory backup repository only), and
 * pkg/service/backup_service.go never dereferences Destination as a
 * filesystem path regardless — it is caller-supplied descriptive metadata
 * (e.g. "where this artifact is meant to end up," an offsite path/bucket
 * name) recorded verbatim on BackupInfo. So a real, writable-looking path
 * here is a faithful request without requiring on-disk I/O to assert against.
 */
function destination(label: string): string {
  return `/tmp/qrdb-e2e-backup-${label}-${Date.now()}`;
}

async function createBackup(
  request: import("@playwright/test").APIRequestContext,
  daemon: { adminUrl: string },
  label: string,
): Promise<BackupResponse> {
  const resp = await request.post(`${daemon.adminUrl}/v1/backups`, {
    data: { destination: destination(label) },
    headers: { "Content-Type": "application/json" },
  });
  if (resp.status() !== 201) {
    throw new Error(
      `POST /v1/backups returned ${resp.status()}: ${await resp.text()}`,
    );
  }
  return resp.json() as Promise<BackupResponse>;
}

test.describe("Backup manager (real backend)", () => {
  test("POST /v1/backups captures a real backup with non-zero size and checksum", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const k = key("create");
    await api.putText(`/kv/${encodeURIComponent(k)}`, "backup-seed-value");

    const backup = await createBackup(request, daemon, "create");

    expect(backup.id).toBeTruthy();
    expect(backup.status).toBe("complete");
    expect(backup.shard_count).toBeGreaterThan(0);
    // Real bytes captured from the live Raft host's shard snapshot export —
    // never zero once at least one shard is loaded.
    expect(backup.size).toBeGreaterThan(0);
    // A real sha256 hex digest computed by the backend over the captured
    // shard bytes (pkg/service/backup_service.go CreateBackup).
    expect(backup.checksum).toMatch(/^[0-9a-f]{64}$/);
    expect(backup.error_message ?? "").toBe("");

    // Cleanup
    await api.delete(`/kv/${encodeURIComponent(k)}`);
    await api.delete(`/v1/backups/${backup.id}`);
  });

  test("GET /v1/backups/list includes the newly created backup", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const backup = await createBackup(request, daemon, "list");

    const list = await api.get<ListBackupsResponse>("/v1/backups/list");
    expect(Array.isArray(list.backups)).toBe(true);
    const found = list.backups.find((b) => b.id === backup.id);
    expect(found, `backup ${backup.id} missing from list`).toBeDefined();
    expect(found!.status).toBe("complete");
    expect(found!.checksum).toBe(backup.checksum);

    // Cleanup
    await api.delete(`/v1/backups/${backup.id}`);
  });

  test("GET /v1/backups/{id} returns matching metadata for the created backup", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const backup = await createBackup(request, daemon, "get");

    const fetched = await api.get<BackupResponse>(`/v1/backups/${backup.id}`);
    expect(fetched.id).toBe(backup.id);
    expect(fetched.size).toBe(backup.size);
    expect(fetched.checksum).toBe(backup.checksum);
    expect(fetched.shard_count).toBe(backup.shard_count);

    // Cleanup
    await api.delete(`/v1/backups/${backup.id}`);
  });

  test("POST /v1/backups/{id}/verify reports a real checksum match", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const k = key("verify");
    await api.putText(`/kv/${encodeURIComponent(k)}`, "verify-seed-value");

    const backup = await createBackup(request, daemon, "verify");

    // Verify recomputes the sha256 over the shard bytes the server holds for
    // this backup and compares it against the checksum recorded at capture
    // time — a real, server-side recomputation, not a canned response.
    const verify = await api.post<VerifyResultResponse>(
      `/v1/backups/${backup.id}/verify`,
      {},
    );
    expect(verify.valid).toBe(true);
    expect(verify.checksum_match).toBe(true);
    expect(verify.expected_checksum).toBe(backup.checksum);
    expect(verify.actual_checksum).toBe(backup.checksum);
    expect(verify.errors ?? []).toEqual([]);

    // Cleanup
    await api.delete(`/kv/${encodeURIComponent(k)}`);
    await api.delete(`/v1/backups/${backup.id}`);
  });

  // GUARDED BEHAVIOR (see the file header comment for the full design):
  // POST /v1/backups/restore against this single-node fixture targets the
  // SOLE shard, which is always the system shard (id 1) on a single-node
  // deployment -- RestoreShardOnline's system-shard guard
  // (service.OnlineRestoreSystemShardError) refuses it unconditionally,
  // regardless of replica count, before ever touching the shard. This test
  // pins that guarded, SAFE contract: the request succeeds at the transport
  // level, reports zero shards restored, carries a per-shard error that
  // identifies the system shard and points the operator at the offline
  // `qrdbd start --restore-backup-id` flow, and — critically — leaves the
  // live data UNCHANGED and the shard itself fully up and serving requests
  // throughout (proven by the KV read immediately after the refused restore
  // call succeeding against the SAME daemon instance, not a restarted one).
  test("POST /v1/backups/restore against the system shard is refused with offline-restore guidance, and the shard stays up", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const k = key("restore");

    await api.putText(`/kv/${encodeURIComponent(k)}`, "before-backup");

    const backup = await createBackup(request, daemon, "restore");

    // Mutate the key AFTER the snapshot was captured. A guard refusal must
    // never touch this.
    await api.putText(`/kv/${encodeURIComponent(k)}`, "after-backup");

    const result = await api.post<RestoreResultResponse>(
      "/v1/backups/restore",
      { backup_id: backup.id },
    );
    expect(result.backup_id).toBe(backup.id);

    // Real backend, real guard: the sole shard on a single-node deployment
    // is always the system shard, so online restore is always refused here.
    expect(result.shards_restored).toBe(0);
    expect(result.errors && result.errors.length).toBeGreaterThan(0);
    expect(result.errors![0]!.message).toContain("system tenant");
    expect(result.errors![0]!.message).toContain("offline");
    expect(result.errors![0]!.message).toContain(
      "qrdbd start --restore-backup-id",
    );

    // Because the restore was correctly refused, the live value must be
    // unchanged -- the endpoint must not silently no-op-succeed.
    const stillMutated = await request.get(
      `${daemon.adminUrl}/kv/${encodeURIComponent(k)}`,
    );
    expect(await stillMutated.text()).toBe("after-backup");

    // The shard — and with it the daemon's auth/data plane — must still be
    // fully up: this is the entire point of the guard (an unguarded
    // stop/import/restart of the system shard would have taken down the
    // data plane serving THIS very request).
    const stillUp = await api.get<{ status: string }>("/health");
    expect(stillUp.status).toBe("ok");

    // Cleanup
    await api.delete(`/kv/${encodeURIComponent(k)}`);
    await api.delete(`/v1/backups/${backup.id}`);
  });

  test("DELETE /v1/backups/{id} removes the backup — subsequent GET returns 404", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const backup = await createBackup(request, daemon, "delete");

    await api.delete(`/v1/backups/${backup.id}`);

    const status = await api.getStatus(`/v1/backups/${backup.id}`);
    expect(status).toBe(404);
  });

  test("GET /v1/backups/{id} returns 404 for an unknown backup id", async ({
    request,
    daemon,
  }) => {
    const api = new ApiClient(request, daemon.adminUrl);
    const status = await api.getStatus("/v1/backups/no-such-backup-xyz");
    expect(status).toBe(404);
  });

  test("POST /v1/backups with an empty destination returns 400", async ({
    request,
    daemon,
  }) => {
    const resp = await request.post(`${daemon.adminUrl}/v1/backups`, {
      data: { destination: "" },
      headers: { "Content-Type": "application/json" },
    });
    expect(resp.status()).toBe(400);
  });

  test("POST /v1/backups/restore with an unknown backup id returns 404", async ({
    request,
    daemon,
  }) => {
    const resp = await request.post(`${daemon.adminUrl}/v1/backups/restore`, {
      data: { backup_id: "no-such-backup-xyz" },
      headers: { "Content-Type": "application/json" },
    });
    expect(resp.status()).toBe(404);
  });
});
