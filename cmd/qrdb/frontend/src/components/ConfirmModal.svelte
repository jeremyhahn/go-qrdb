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
  import { AlertTriangle } from "lucide-svelte";

  let {
    open = false,
    title = "Confirm",
    message = "Are you sure?",
    confirmLabel = "Confirm",
    cancelLabel = "Cancel",
    variant = "error",
    onconfirm,
    oncancel,
  }: {
    open?: boolean;
    title?: string;
    message?: string;
    confirmLabel?: string;
    cancelLabel?: string;
    variant?: "error" | "warning" | "primary";
    onconfirm?: () => void;
    oncancel?: () => void;
  } = $props();

  const btnVariants: Readonly<Record<string, string>> = {
    error: "btn-error",
    warning: "btn-warning",
    primary: "btn-primary",
  } as const;
</script>

{#if open}
  <dialog class="modal modal-open">
    <div class="modal-box border border-base-content/6">
      <h3 class="text-base font-semibold">
        {#if variant === "error" || variant === "warning"}<AlertTriangle
            class="h-5 w-5 text-warning inline mr-2"
          />{/if}{title}
      </h3>
      <p class="py-4">{message}</p>
      <div class="modal-action">
        <button class="btn btn-ghost" onclick={oncancel}>{cancelLabel}</button>
        <button
          class="btn {btnVariants[variant] ?? 'btn-primary'}"
          onclick={onconfirm}
        >
          {confirmLabel}
        </button>
      </div>
    </div>
    <form method="dialog" class="modal-backdrop">
      <button onclick={oncancel}>close</button>
    </form>
  </dialog>
{/if}
