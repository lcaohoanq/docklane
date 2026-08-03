<script lang="ts">
  import { tick } from "svelte";

  let {
    open,
    title,
    description,
    confirmLabel,
    cancelLabel = "Cancel",
    danger = false,
    busy = false,
    alert = false,
    onConfirm,
    onCancel,
  }: {
    open: boolean;
    title: string;
    description: string;
    confirmLabel: string;
    cancelLabel?: string;
    danger?: boolean;
    busy?: boolean;
    alert?: boolean;
    onConfirm: () => void;
    onCancel: () => void;
  } = $props();

  let cancelButton = $state<HTMLButtonElement>();

  $effect(() => {
    if (open) void tick().then(() => cancelButton?.focus());
  });

  function trapFocus(event: KeyboardEvent) {
    if (event.key === "Escape") {
      onCancel();
      return;
    }
    if (event.key !== "Tab") return;
    const modal = event.currentTarget as HTMLElement;
    const focusable = Array.from(
      modal.querySelectorAll<HTMLElement>(
        "button:not([disabled]), input:not([disabled]), select:not([disabled]), [href]",
      ),
    );
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }
</script>

{#if open}
  <div class="modal modal-open modal-layer" role="presentation" onkeydown={trapFocus}>
    <button class="modal-backdrop" type="button" aria-label={cancelLabel} onclick={onCancel}
    ></button>
    <div
      class="modal-box confirm-dialog"
      role={alert ? "alertdialog" : "dialog"}
      aria-modal="true"
      aria-labelledby="confirm-dialog-title"
      aria-describedby="confirm-dialog-description"
    >
      {#if danger}<span class="danger-icon"
          ><svg viewBox="0 0 24 24" aria-hidden="true"
            ><path d="M12 8v5M12 17h.01"></path><path
              d="M10.3 4.9 3.6 17a2 2 0 0 0 1.8 3h13.2a2 2 0 0 0 1.8-3L13.7 4.9a2 2 0 0 0-3.4 0Z"
            ></path></svg
          ></span
        >{/if}
      <h2 id="confirm-dialog-title">{title}</h2>
      <p id="confirm-dialog-description">{description}</p>
      <div class="confirm-actions">
        <button
          bind:this={cancelButton}
          class="btn btn-outline secondary"
          type="button"
          onclick={onCancel}>{cancelLabel}</button
        >
        <button
          class:btn-error={danger}
          class:danger-solid={danger}
          class="btn"
          type="button"
          disabled={busy}
          onclick={onConfirm}
          >{busy ? `${confirmLabel.replace(/ route$/, "")}…` : confirmLabel}</button
        >
      </div>
    </div>
  </div>
{/if}
