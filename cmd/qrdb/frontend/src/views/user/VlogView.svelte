<!--
  Copyright 2026 Jeremy Hahn
  SPDX-License-Identifier: Apache-2.0

  Licensed under the Apache License, Version 2.0 (the "License");
  you may not use this file except in compliance with the License.
  You may obtain a copy of the License at

      http://www.apache.org/licenses/LICENSE-2.0

  Unless required by applicable law or agreed to in writing, software
  distributed under the License is distributed on an "AS IS" BASIS,
  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
  See the License for the specific language governing permissions and
  limitations under the License.
-->
<script lang="ts">
  import { untrack } from "svelte";
  import type {
    Client,
    VlogConsistencyProof,
    VlogEntry,
    VlogHead,
    VlogInclusionProof,
    VlogSignatureType,
    VlogSignHeadResult,
  } from "@qrdb/sdk";
  import ConfirmModal from "../../components/ConfirmModal.svelte";
  import PageHeader from "../../components/PageHeader.svelte";
  import { showToast } from "../../stores/toast.svelte.ts";

  let { client }: { client: Client } = $props();

  type KeyChoice = VlogSignatureType | "none";

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------

  let logs: string[] = $state([]);
  let selectedLog: string | null = $state(null);
  let loading: boolean = $state(false);

  // Create form
  let createName: string = $state("");
  let createOrigin: string = $state("");
  let createKeyType: KeyChoice = $state("ed25519");
  let createAllowExpire: boolean = $state(false);
  let createdKeyVersion: number | null = $state(null);

  // Selected log
  let head: VlogHead | null = $state(null);
  let appendText: string = $state("");
  let getFrom: string = $state("0");
  let getLimit: string = $state("100");
  let entries: VlogEntry[] = $state([]);
  let inclusionIndex: string = $state("0");
  let inclusionSize: string = $state("1");
  let inclusionProof: VlogInclusionProof | null = $state(null);
  let consistencySize1: string = $state("1");
  let consistencySize2: string = $state("1");
  let consistencyProof: VlogConsistencyProof | null = $state(null);
  let signedHead: VlogSignHeadResult | null = $state(null);
  let rotateKeyType: VlogSignatureType = $state("ed25519");
  let rotatedKeyVersion: number | null = $state(null);
  let expireCutoff: string = $state("0");
  let confirmExpire: boolean = $state(false);

  // ---------------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------------

  function errorText(err: unknown): string {
    return err instanceof Error ? err.message : String(err);
  }

  /** Parses a non-negative integer field; shows a warning and returns null otherwise. */
  function parseCount(value: string, field: string): number | null {
    const text = String(value).trim();
    if (!/^\d+$/.test(text)) {
      showToast("warning", `${field} must be a non-negative integer.`);
      return null;
    }
    return Number(text);
  }

  function encodeBody(text: string): string {
    const bytes = new TextEncoder().encode(text);
    let binary = "";
    for (const b of bytes) binary += String.fromCharCode(b);
    return btoa(binary);
  }

  function decodeBody(b64: string): string {
    try {
      const binary = atob(b64);
      const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0));
      return new TextDecoder().decode(bytes);
    } catch {
      return b64;
    }
  }

  function resetSelection(): void {
    selectedLog = null;
    head = null;
    entries = [];
    inclusionProof = null;
    consistencyProof = null;
    signedHead = null;
    rotatedKeyVersion = null;
    confirmExpire = false;
  }

  // ---------------------------------------------------------------------------
  // Actions
  // ---------------------------------------------------------------------------

  async function loadLogs(): Promise<void> {
    loading = true;
    try {
      const result = await client.vlog.list();
      logs = [...result.logs];
    } catch (err: unknown) {
      showToast("error", errorText(err));
    } finally {
      loading = false;
    }
  }

  async function loadHead(): Promise<void> {
    if (selectedLog === null) return;
    try {
      head = await client.vlog.head(selectedLog);
    } catch (err: unknown) {
      showToast("error", errorText(err));
    }
  }

  async function selectLog(log: string): Promise<void> {
    resetSelection();
    selectedLog = log;
    await loadHead();
  }

  async function handleCreate(): Promise<void> {
    const log = createName.trim();
    const origin = createOrigin.trim();
    if (log === "" || origin === "") {
      showToast("warning", "Log name and origin are required.");
      return;
    }
    try {
      const result = await client.vlog.create({
        log,
        origin,
        allowExpireBodies: createAllowExpire,
        ...(createKeyType !== "none" ? { signingKeyType: createKeyType } : {}),
      });
      createdKeyVersion = result.key?.version ?? null;
      showToast("success", `Log "${result.log}" created.`);
      createName = "";
      createOrigin = "";
      await loadLogs();
    } catch (err: unknown) {
      showToast("error", errorText(err));
    }
  }

  async function handleAppend(): Promise<void> {
    if (selectedLog === null) return;
    const lines = appendText.split("\n").filter((line) => line !== "");
    if (lines.length === 0) {
      showToast("warning", "Enter at least one entry.");
      return;
    }
    try {
      const result = await client.vlog.append({ log: selectedLog, entries: lines.map(encodeBody) });
      showToast("success", `Appended ${result.count} entries from index ${result.firstIndex}`);
      appendText = "";
      await loadHead();
    } catch (err: unknown) {
      showToast("error", errorText(err));
    }
  }

  async function handleGet(): Promise<void> {
    if (selectedLog === null) return;
    const from = parseCount(getFrom, "From");
    if (from === null) return;
    const limit = parseCount(getLimit, "Limit");
    if (limit === null) return;
    try {
      const result = await client.vlog.get({ log: selectedLog, from, limit });
      entries = [...result.entries];
    } catch (err: unknown) {
      showToast("error", errorText(err));
    }
  }

  async function handleInclusion(): Promise<void> {
    if (selectedLog === null) return;
    const index = parseCount(inclusionIndex, "Index");
    if (index === null) return;
    const size = parseCount(inclusionSize, "Size");
    if (size === null) return;
    try {
      inclusionProof = await client.vlog.proveInclusion({ log: selectedLog, index, size });
    } catch (err: unknown) {
      showToast("error", errorText(err));
    }
  }

  async function handleConsistency(): Promise<void> {
    if (selectedLog === null) return;
    const size1 = parseCount(consistencySize1, "Size 1");
    if (size1 === null) return;
    const size2 = parseCount(consistencySize2, "Size 2");
    if (size2 === null) return;
    try {
      consistencyProof = await client.vlog.proveConsistency({ log: selectedLog, size1, size2 });
    } catch (err: unknown) {
      showToast("error", errorText(err));
    }
  }

  async function handleSignHead(): Promise<void> {
    if (selectedLog === null) return;
    try {
      signedHead = await client.vlog.signHead(selectedLog);
      await loadHead();
    } catch (err: unknown) {
      showToast("error", errorText(err));
    }
  }

  async function handleRotate(): Promise<void> {
    if (selectedLog === null) return;
    try {
      const result = await client.vlog.rotateKey({ log: selectedLog, signingKeyType: rotateKeyType });
      rotatedKeyVersion = result.key.version;
      showToast("success", `Head-signing key rotated to version ${result.key.version}.`);
    } catch (err: unknown) {
      showToast("error", errorText(err));
    }
  }

  function requestExpire(): void {
    if (selectedLog === null) return;
    if (parseCount(expireCutoff, "Cutoff") === null) return;
    confirmExpire = true;
  }

  async function handleExpire(): Promise<void> {
    confirmExpire = false;
    if (selectedLog === null) return;
    const cutoff = parseCount(expireCutoff, "Cutoff");
    if (cutoff === null) return;
    try {
      const result = await client.vlog.expireBodies({ log: selectedLog, cutoff });
      showToast("success", `Expired ${result.expired} bodies`);
      await loadHead();
    } catch (err: unknown) {
      showToast("error", errorText(err));
    }
  }

  // A new client is a new database: drop the selection and list its logs.
  $effect(() => {
    void client;
    untrack(() => {
      resetSelection();
      createdKeyVersion = null;
      void loadLogs();
    });
  });
</script>

<div class="flex flex-col gap-6">
  <PageHeader title="Verifiable Logs" description="Append-only Merkle logs with inclusion and consistency proofs">
    {#snippet actions()}
      <button class="btn btn-outline btn-sm" onclick={() => void loadLogs()}>Refresh</button>
    {/snippet}
  </PageHeader>

  <div class="flex gap-6">
    <!-- Log list and create form -->
    <div class="flex w-80 flex-col gap-4">
      <div class="card bg-base-200 shadow-sm">
        <div class="card-body p-5">
          <h3 class="card-title text-base font-semibold">Logs</h3>
          <div class="flex flex-col gap-1" data-testid="vlog-list">
            {#each logs as log (log)}
              <button
                class="btn btn-sm justify-start {selectedLog === log ? 'btn-primary' : 'btn-ghost'}"
                onclick={() => void selectLog(log)}
              >
                {log}
              </button>
            {:else}
              <p class="text-base-content/50 text-sm">{loading ? "Loading…" : "No logs."}</p>
            {/each}
          </div>
        </div>
      </div>

      <div class="card bg-base-200 shadow-sm">
        <div class="card-body p-5">
          <h3 class="card-title text-base font-semibold">Create Log</h3>
          <label class="form-control w-full">
            <div class="label"><span class="label-text text-xs">Log name</span></div>
            <input type="text" class="input input-bordered input-sm w-full" bind:value={createName} />
          </label>
          <label class="form-control w-full">
            <div class="label"><span class="label-text text-xs">Origin</span></div>
            <input
              type="text"
              class="input input-bordered input-sm w-full"
              placeholder="example.com/log"
              bind:value={createOrigin}
            />
          </label>
          <label class="form-control w-full">
            <div class="label"><span class="label-text text-xs">Signing key type</span></div>
            <select class="select select-bordered select-sm w-full" bind:value={createKeyType}>
              <option value="ed25519">ed25519</option>
              <option value="ecdsa-p256-sha256">ecdsa-p256-sha256</option>
              <option value="none">none</option>
            </select>
          </label>
          <label class="label cursor-pointer justify-start gap-2">
            <input type="checkbox" class="checkbox checkbox-sm" bind:checked={createAllowExpire} />
            <span class="label-text text-xs">Allow expiring bodies</span>
          </label>
          <button class="btn btn-primary btn-sm" data-testid="vlog-create-submit" onclick={() => void handleCreate()}>
            Create
          </button>
          {#if createdKeyVersion !== null}
            <p class="text-xs" data-testid="vlog-created-key">Key version {createdKeyVersion}</p>
          {/if}
        </div>
      </div>
    </div>

    <!-- Selected log -->
    <div class="flex flex-1 flex-col gap-4">
      {#if selectedLog === null}
        <p class="text-base-content/50 text-sm">Select a log to work with it.</p>
      {:else}
        <div class="card bg-base-200 shadow-sm">
          <div class="card-body p-5">
            <h3 class="card-title text-base font-semibold">Head of {selectedLog}</h3>
            <div data-testid="vlog-head">
              {#if head !== null}
                <table class="table table-sm">
                  <tbody>
                    <tr><th class="w-36">Size</th><td>{head.size}</td></tr>
                    <tr><th>Root</th><td class="font-mono text-xs break-all">{head.root}</td></tr>
                    <tr><th>Bodies from</th><td>{head.bodiesFrom}</td></tr>
                    <tr><th>Checkpoint size</th><td>{head.checkpointSize}</td></tr>
                    <tr>
                      <th>Checkpoint</th>
                      <td class="font-mono text-xs whitespace-pre-wrap break-all">
                        {head.checkpoint !== undefined ? decodeBody(head.checkpoint) : "none"}
                      </td>
                    </tr>
                  </tbody>
                </table>
              {/if}
            </div>
          </div>
        </div>

        <div class="card bg-base-200 shadow-sm">
          <div class="card-body p-5">
            <h3 class="card-title text-base font-semibold">Append</h3>
            <textarea
              class="textarea textarea-bordered w-full font-mono text-xs"
              rows="4"
              placeholder="one entry per line"
              aria-label="Entries to append"
              bind:value={appendText}
            ></textarea>
            <button class="btn btn-primary btn-sm self-start" data-testid="vlog-append-submit" onclick={() => void handleAppend()}>
              Append
            </button>
          </div>
        </div>

        <div class="card bg-base-200 shadow-sm">
          <div class="card-body p-5">
            <h3 class="card-title text-base font-semibold">Entries</h3>
            <div class="flex items-end gap-3">
              <label class="form-control">
                <div class="label"><span class="label-text text-xs">From</span></div>
                <input type="text" inputmode="numeric" class="input input-bordered input-sm w-28" aria-label="From" bind:value={getFrom} />
              </label>
              <label class="form-control">
                <div class="label"><span class="label-text text-xs">Limit</span></div>
                <input type="text" inputmode="numeric" class="input input-bordered input-sm w-28" aria-label="Limit" bind:value={getLimit} />
              </label>
              <button class="btn btn-outline btn-sm" data-testid="vlog-get-submit" onclick={() => void handleGet()}>Get</button>
            </div>
            <ul class="flex flex-col gap-1 font-mono text-xs" data-testid="vlog-entries">
              {#each entries as entry (entry.index)}
                <li>
                  <span class="text-base-content/60">{entry.index}:</span>
                  {entry.expired || entry.body === undefined ? "expired" : decodeBody(entry.body)}
                </li>
              {/each}
            </ul>
          </div>
        </div>

        <div class="flex gap-4">
          <div class="card bg-base-200 flex-1 shadow-sm">
            <div class="card-body p-5">
              <h3 class="card-title text-base font-semibold">Inclusion Proof</h3>
              <div class="flex items-end gap-3">
                <label class="form-control">
                  <div class="label"><span class="label-text text-xs">Index</span></div>
                  <input type="text" inputmode="numeric" class="input input-bordered input-sm w-24" aria-label="Inclusion index" bind:value={inclusionIndex} />
                </label>
                <label class="form-control">
                  <div class="label"><span class="label-text text-xs">Size</span></div>
                  <input type="text" inputmode="numeric" class="input input-bordered input-sm w-24" aria-label="Inclusion size" bind:value={inclusionSize} />
                </label>
                <button class="btn btn-outline btn-sm" data-testid="vlog-inclusion-submit" onclick={() => void handleInclusion()}>Prove</button>
              </div>
              <div data-testid="vlog-inclusion-proof">
                {#if inclusionProof !== null}
                  <p class="text-xs">{inclusionProof.hashes.length} hashes</p>
                  <ul class="font-mono text-xs break-all">
                    {#each inclusionProof.hashes as hash, i (i)}<li>{hash}</li>{/each}
                  </ul>
                {/if}
              </div>
            </div>
          </div>

          <div class="card bg-base-200 flex-1 shadow-sm">
            <div class="card-body p-5">
              <h3 class="card-title text-base font-semibold">Consistency Proof</h3>
              <div class="flex items-end gap-3">
                <label class="form-control">
                  <div class="label"><span class="label-text text-xs">Size 1</span></div>
                  <input type="text" inputmode="numeric" class="input input-bordered input-sm w-24" aria-label="Consistency size 1" bind:value={consistencySize1} />
                </label>
                <label class="form-control">
                  <div class="label"><span class="label-text text-xs">Size 2</span></div>
                  <input type="text" inputmode="numeric" class="input input-bordered input-sm w-24" aria-label="Consistency size 2" bind:value={consistencySize2} />
                </label>
                <button class="btn btn-outline btn-sm" data-testid="vlog-consistency-submit" onclick={() => void handleConsistency()}>Prove</button>
              </div>
              <div data-testid="vlog-consistency-proof">
                {#if consistencyProof !== null}
                  <p class="text-xs">{consistencyProof.hashes.length} hashes</p>
                  <ul class="font-mono text-xs break-all">
                    {#each consistencyProof.hashes as hash, i (i)}<li>{hash}</li>{/each}
                  </ul>
                {/if}
              </div>
            </div>
          </div>
        </div>

        <div class="card bg-base-200 shadow-sm">
          <div class="card-body p-5">
            <h3 class="card-title text-base font-semibold">Signing</h3>
            <div class="flex flex-wrap items-end gap-3">
              <button class="btn btn-outline btn-sm" data-testid="vlog-sign-head" onclick={() => void handleSignHead()}>Sign head</button>
              <label class="form-control">
                <div class="label"><span class="label-text text-xs">New key type</span></div>
                <select class="select select-bordered select-sm" aria-label="New key type" bind:value={rotateKeyType}>
                  <option value="ed25519">ed25519</option>
                  <option value="ecdsa-p256-sha256">ecdsa-p256-sha256</option>
                </select>
              </label>
              <button class="btn btn-outline btn-sm" data-testid="vlog-rotate-submit" onclick={() => void handleRotate()}>Rotate key</button>
              {#if rotatedKeyVersion !== null}
                <span class="text-xs" data-testid="vlog-rotated-key">Key version {rotatedKeyVersion}</span>
              {/if}
            </div>
            <div data-testid="vlog-signed-head">
              {#if signedHead !== null}
                <p class="text-xs">Key version {signedHead.head.keyVersion}, {signedHead.head.signatureType}</p>
                <pre class="bg-base-300 rounded p-2 font-mono text-xs whitespace-pre-wrap break-all">{decodeBody(signedHead.checkpoint)}</pre>
              {/if}
            </div>
          </div>
        </div>

        <div class="card bg-base-200 shadow-sm">
          <div class="card-body p-5">
            <h3 class="card-title text-base font-semibold">Expire Bodies</h3>
            <div class="flex items-end gap-3">
              <label class="form-control">
                <div class="label"><span class="label-text text-xs">Cutoff index</span></div>
                <input type="text" inputmode="numeric" class="input input-bordered input-sm w-28" aria-label="Cutoff index" bind:value={expireCutoff} />
              </label>
              <button class="btn btn-error btn-outline btn-sm" data-testid="vlog-expire-submit" onclick={requestExpire}>Expire</button>
            </div>
          </div>
        </div>
      {/if}
    </div>
  </div>
</div>

<ConfirmModal
  open={confirmExpire}
  title="Expire Bodies"
  message="Remove the bodies of the entries before index {expireCutoff} in &quot;{selectedLog ?? ''}&quot;? This cannot be undone."
  confirmLabel="Expire"
  variant="error"
  onconfirm={() => void handleExpire()}
  oncancel={() => { confirmExpire = false; }}
/>
