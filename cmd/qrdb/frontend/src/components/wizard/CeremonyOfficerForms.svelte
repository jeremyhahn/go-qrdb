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

  CeremonyOfficerForms.svelte
  ============================
  Collects the initial share holders a ceremony.initialize call splits the
  system barrier's unseal key to: one form per holder (username, role,
  CSR). Unlike HolderEnrolmentForms (post-init enrolment via
  officers.registerOfficer), this component makes NO server call per
  holder: `qrdb init` is the ONLY system bootstrap, so a holder cannot be
  registered before it -- every holder here is collected locally and sent
  together in the single ceremony.initialize call the wizard makes once
  every form is filled in. The server issues each holder's certificate in
  that SAME response (see ServerInitWizard's "complete" step), not here.

  No share and no holder private key ever passes through this component: a
  holder claims its sealed envelope later, with its own keys, from the
  qrdb CLI or the desktop app.
-->
<script lang="ts">
  import type { CeremonyOfficerRequest, HolderRole } from "@qrdb/sdk";
  import { AlertCircle, UserPlus } from "lucide-svelte";

  let {
    count,
    officers = $bindable([]),
  }: {
    /** Number of holders to collect: the Shamir share count (N). */
    count: number;
    /** Every filled-in, valid holder; bound by the caller. */
    officers?: CeremonyOfficerRequest[];
  } = $props();

  /** Per-holder form state. */
  interface HolderForm {
    username: string;
    role: HolderRole;
    csrPem: string;
  }

  function emptyForm(): HolderForm {
    return { username: "", role: "so", csrPem: "" };
  }

  // Seeded from the incoming `officers` prop, not empty: the wizard's
  // "officers" step unmounts and remounts this component on every visit
  // (a Svelte {#if} branch), and the already-collected holders must
  // survive navigating forward to "admin" and back, not just the
  // `officers` array the caller holds onto -- the forms themselves.
  let forms: HolderForm[] = $state(
    officers.map((o) => ({ username: o.username, role: o.role ?? "so", csrPem: o.csrPem })),
  );

  // Keep one form per holder when the share count changes, preserving
  // what is already filled in.
  $effect(() => {
    const n = Math.max(0, count);
    if (forms.length !== n) {
      forms = Array.from({ length: n }, (_, i) => forms[i] ?? emptyForm());
    }
  });

  /** A form is complete once both its username and CSR are non-empty. */
  function isComplete(f: HolderForm): boolean {
    return f.username.trim() !== "" && f.csrPem.trim() !== "";
  }

  $effect(() => {
    officers = forms.filter(isComplete).map((f) => ({
      username: f.username.trim(),
      role: f.role,
      csrPem: f.csrPem.trim(),
    }));
  });

  let filledCount = $derived(forms.filter(isComplete).length);
</script>

<div class="flex flex-col gap-4">
  <div class="flex items-center justify-between text-sm">
    <span class="font-semibold">Share Holders</span>
    <span
      class="badge"
      class:badge-success={filledCount === count && count > 0}
      class:badge-warning={filledCount !== count}
      data-testid="officer-form-count"
    >
      {filledCount} / {count}
    </span>
  </div>

  {#each forms as form, i (i)}
    <div
      class="card border bg-base-200"
      class:border-success={isComplete(form)}
      class:border-base-300={!isComplete(form)}
      data-testid={`officer-form-${i}`}
    >
      <div class="card-body gap-3 p-4">
        <div class="flex items-center gap-2">
          <UserPlus class="text-base-content/50 h-5 w-5 shrink-0" />
          <h3 class="font-semibold text-sm">Holder {i + 1}</h3>
        </div>

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
            data-testid={`officer-csr-${i}`}
          ></textarea>
        </div>

        {#if !isComplete(form) && (form.username.trim() !== "" || form.csrPem.trim() !== "")}
          <div role="alert" class="alert alert-warning py-2 text-xs" data-testid={`officer-incomplete-${i}`}>
            <AlertCircle class="h-3 w-3 shrink-0" />
            <span>Username and CSR PEM are both required.</span>
          </div>
        {/if}
      </div>
    </div>
  {/each}
</div>
