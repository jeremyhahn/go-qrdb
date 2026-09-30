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

  HolderEnrolmentForms.svelte
  ===========================
  Enrols the unseal-domain share holders a Shamir initialization splits the
  unseal key to: one form per holder (username, role, CSR), each registered
  with the server, which returns the CA-issued certificate the admin hands to
  the holder. Shamir initialization requires exactly `count` enrolled holders,
  so the caller gates its "Initialize" action on `registeredCount === count`.

  No share and no holder private key ever passes through this component: a
  holder claims its sealed envelope later, with its own keys, from the qrdb
  CLI or the desktop app.
-->
<script lang="ts">
  import type { Client, HolderRole, OfficerRegisterResponse } from "@qrdb/sdk";
  import { AlertCircle, CheckCircle, Copy, Download, UserPlus } from "lucide-svelte";

  let {
    client,
    count,
    registeredCount = $bindable(0),
  }: {
    client: Client;
    /** Number of holders to enrol: the Shamir share count (N). */
    count: number;
    /** How many of the holders are enrolled; bound by the caller. */
    registeredCount?: number;
  } = $props();

  /** Per-holder enrolment form state. */
  interface HolderForm {
    username: string;
    role: HolderRole;
    csrPem: string;
    result: OfficerRegisterResponse | null;
    error: string;
    loading: boolean;
    certCopied: boolean;
  }

  function emptyForm(): HolderForm {
    return { username: "", role: "so", csrPem: "", result: null, error: "", loading: false, certCopied: false };
  }

  let forms: HolderForm[] = $state([]);

  // Keep one form per holder when the share count changes, preserving the
  // forms already filled in or registered.
  $effect(() => {
    const n = Math.max(0, count);
    if (forms.length !== n) {
      forms = Array.from({ length: n }, (_, i) => forms[i] ?? emptyForm());
    }
  });

  $effect(() => {
    registeredCount = forms.filter((f) => f.result !== null).length;
  });

  async function register(index: number): Promise<void> {
    const form = forms[index];
    if (form === undefined) return;
    const username = form.username.trim();
    const csrPem = form.csrPem.trim();
    if (username === "") {
      forms[index] = { ...form, error: "Username is required." };
      return;
    }
    if (csrPem === "") {
      forms[index] = { ...form, error: "CSR PEM is required." };
      return;
    }
    forms[index] = { ...form, loading: true, error: "" };
    try {
      const result = await client.officers.registerOfficer({ username, role: form.role, csrPem });
      forms[index] = { ...forms[index]!, loading: false, result, error: "" };
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Registration failed";
      forms[index] = { ...forms[index]!, loading: false, error: msg };
    }
  }

  function copyCert(index: number): void {
    const form = forms[index];
    if (form === undefined || form.result === null) return;
    void navigator.clipboard.writeText(form.result.certPem).then(() => {
      const current = forms[index];
      if (current !== undefined) forms[index] = { ...current, certCopied: true };
    });
  }

  function downloadCert(index: number): void {
    const form = forms[index];
    if (form === undefined || form.result === null) return;
    const blob = new Blob([form.result.certPem], { type: "application/x-pem-file" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `holder-${form.username}-cert.pem`;
    a.click();
    URL.revokeObjectURL(url);
  }
</script>

<div class="flex flex-col gap-4">
  <div class="flex items-center justify-between text-sm">
    <span class="font-semibold">Enrolled Holders</span>
    <span
      class="badge"
      class:badge-success={registeredCount === count && count > 0}
      class:badge-warning={registeredCount !== count}
      data-testid="officer-registration-count"
    >
      {registeredCount} / {count}
    </span>
  </div>

  {#each forms as form, i (i)}
    <div
      class="card border bg-base-200"
      class:border-success={form.result !== null}
      class:border-base-300={form.result === null}
      data-testid={`officer-form-${i}`}
    >
      <div class="card-body gap-3 p-4">
        <div class="flex items-center gap-2">
          {#if form.result !== null}
            <CheckCircle class="text-success h-5 w-5 shrink-0" />
          {:else}
            <UserPlus class="text-base-content/50 h-5 w-5 shrink-0" />
          {/if}
          <h3 class="font-semibold text-sm">
            Holder {i + 1}
            {#if form.result !== null}
              <span class="text-base-content/60 font-normal">
                — {form.username} ({form.result.role === "custodian" ? "key custodian" : "Security Officer"})
              </span>
            {/if}
          </h3>
        </div>

        {#if form.result === null}
          <div class="form-control">
            <label class="label" for={`officer-username-${i}`}>
              <span class="label-text text-xs">Username</span>
            </label>
            <input
              id={`officer-username-${i}`}
              type="text"
              class="input input-bordered input-sm"
              placeholder="alice"
              value={form.username}
              oninput={(e: Event) => { if (forms[i]) forms[i].username = (e.currentTarget as HTMLInputElement).value; }}
              disabled={form.loading}
              data-testid={`officer-username-${i}`}
            />
          </div>

          <div class="form-control">
            <label class="label" for={`officer-role-${i}`}>
              <span class="label-text text-xs">Role</span>
            </label>
            <select
              id={`officer-role-${i}`}
              class="select select-bordered select-sm"
              value={form.role}
              onchange={(e: Event) => { if (forms[i]) forms[i].role = (e.currentTarget as HTMLSelectElement).value as HolderRole; }}
              disabled={form.loading}
              data-testid={`officer-role-${i}`}
            >
              <option value="so">Security Officer</option>
              <option value="custodian">Key custodian</option>
            </select>
          </div>

          <div class="form-control">
            <label class="label" for={`officer-csr-${i}`}>
              <span class="label-text text-xs">CSR PEM (the holder's public key, generated offline)</span>
            </label>
            <textarea
              id={`officer-csr-${i}`}
              class="textarea textarea-bordered textarea-sm font-mono text-xs leading-tight"
              rows={5}
              placeholder={"-----BEGIN CERTIFICATE REQUEST-----\n...\n-----END CERTIFICATE REQUEST-----"}
              value={form.csrPem}
              oninput={(e: Event) => { if (forms[i]) forms[i].csrPem = (e.currentTarget as HTMLTextAreaElement).value; }}
              disabled={form.loading}
              data-testid={`officer-csr-${i}`}
            ></textarea>
          </div>

          {#if form.error}
            <div role="alert" class="alert alert-error py-2 text-xs" data-testid={`officer-error-${i}`}>
              <AlertCircle class="h-3 w-3 shrink-0" />
              <span>{form.error}</span>
            </div>
          {/if}

          <button
            class="btn btn-primary btn-sm self-end"
            onclick={() => void register(i)}
            disabled={form.loading || form.username.trim() === "" || form.csrPem.trim() === ""}
            data-testid={`register-officer-btn-${i}`}
          >
            {#if form.loading}
              <span class="loading loading-spinner loading-xs"></span>
            {/if}
            Enrol Holder
          </button>
        {:else}
          <div class="form-control">
            <div class="flex items-center justify-between mb-1">
              <span class="label-text text-xs font-semibold">Issued Certificate PEM</span>
              <div class="flex gap-1">
                <button
                  class="btn btn-ghost btn-xs gap-1"
                  onclick={() => copyCert(i)}
                  aria-label="Copy certificate PEM"
                  data-testid={`copy-cert-btn-${i}`}
                >
                  <Copy class="h-3 w-3" />
                  {form.certCopied ? "Copied" : "Copy"}
                </button>
                <button
                  class="btn btn-ghost btn-xs gap-1"
                  onclick={() => downloadCert(i)}
                  aria-label="Download certificate PEM"
                  data-testid={`download-cert-btn-${i}`}
                >
                  <Download class="h-3 w-3" />
                  Download
                </button>
              </div>
            </div>
            <textarea
              class="textarea textarea-bordered textarea-xs font-mono text-xs leading-tight bg-base-300"
              rows={4}
              readonly
              value={form.result.certPem}
              data-testid={`officer-cert-${i}`}
            ></textarea>
            <p class="text-base-content/50 mt-1 text-xs">
              Hand this certificate to the holder out of band. The holder uses it to claim its
              envelope from the qrdb CLI or the desktop app.
            </p>
          </div>
        {/if}
      </div>
    </div>
  {/each}
</div>
