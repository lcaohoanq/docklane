<script lang="ts">
  let { notice, error, onDismissNotice, onDismissError }: {
    notice: string;
    error: string;
    onDismissNotice: () => void;
    onDismissError: () => void;
  } = $props();
</script>

{#if notice}
  <div class="toast toast-top toast-center" role="status" aria-live="polite"><div class="alert alert-success">
    <span>{notice}</span>
    <button class="btn btn-circle btn-ghost btn-sm" type="button" aria-label="Dismiss notification" onclick={onDismissNotice}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m6 6 12 12M18 6 6 18"></path></svg></button>
  </div></div>
{/if}
{#if error}
  <div class="toast toast-top toast-center" role="alert" aria-live="assertive"><div class="alert alert-error">
    <span>{error}</span>
    <button class="btn btn-circle btn-ghost btn-sm" type="button" aria-label="Dismiss error" onclick={onDismissError}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m6 6 12 12M18 6 6 18"></path></svg></button>
  </div></div>
{/if}
