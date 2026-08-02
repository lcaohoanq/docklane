<script lang="ts">
  let { total, ready, publishing, attention }: { total: number; ready: number; publishing: number; attention: number } = $props();
</script>

<div class="route-summary" aria-label="Route status summary">
  <div><strong>{total}</strong><span>Total routes</span></div>
  <div><i class="summary-dot ready"></i><strong>{ready}</strong><span>Ready</span></div>
  <div><i class="summary-dot publishing"></i><strong>{publishing}</strong><span>Publishing</span></div>
  <div><i class="summary-dot attention"></i><strong>{attention}</strong><span>Needs attention</span></div>
</div>
