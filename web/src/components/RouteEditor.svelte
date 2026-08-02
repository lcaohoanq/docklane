<script lang="ts">
  import { tick } from "svelte";
  import ConfirmDialog from "./ConfirmDialog.svelte";
  import { createRoute, updateRoute } from "../lib/api";
  import { uniquePorts } from "../lib/container-utils";
  import { availableRouteName, recommendedPort, recommendedScheme, routeNameIssue, selectorFor, slug } from "../lib/route-utils";
  import type { Container, Route } from "../lib/types";

  let { selected, editing, routes, baseDomain, onClose, onSaved, onError }: {
    selected: Container | null;
    editing: Route | null;
    routes: Route[];
    baseDomain: string;
    onClose: () => void;
    onSaved: (message: string, routeId: number | null) => void | Promise<void>;
    onError: (message: string) => void;
  } = $props();

  let routeName = $state("");
  let routePort = $state(80);
  let routeScheme = $state("http");
  let saving = $state(false);
  let attempted = $state(false);
  let editorError = $state("");
  let commandCopied = $state(false);
  let discardOpen = $state(false);
  let initialState = $state("");
  let loadedKey = $state("");
  let firstInput = $state<HTMLInputElement>();

  const targetKey = $derived(editing ? `route:${editing.id}:${editing.revision}` : selected ? `container:${selected.id}` : "");
  const issue = $derived(routeNameIssue(routeName, routes, editing?.id));
  const nameConflict = $derived(routes.some((route) => route.name === routeName && route.id !== editing?.id));
  const invalidPort = $derived(!!selected && !selected.exposedPorts.includes(Number(routePort)));
  const dirty = $derived(initialState !== "" && editorState() !== initialState);
  const command = $derived(commandFor());

  $effect(() => {
    if (!targetKey || loadedKey === targetKey) return;
    loadedKey = targetKey;
    routeName = editing ? editing.name : availableRouteName(slug(selected?.composeService || selected?.name || ""), routes);
    routePort = editing ? editing.port : recommendedPort(selected?.exposedPorts || []);
    routeScheme = editing ? editing.scheme : recommendedScheme(routePort);
    attempted = false;
    editorError = "";
    commandCopied = false;
    initialState = editorState();
    void tick().then(() => firstInput?.focus());
  });

  function editorState() {
    return JSON.stringify({ name: routeName, port: Number(routePort), scheme: routeScheme });
  }

  function portRecommendation() {
    if (!selected) return "";
    const available = uniquePorts(selected.exposedPorts);
    if (available.length === 0) return "This container declares no internal TCP port. Add `expose` to its Compose service, recreate it, then refresh Docker.";
    if (routePort === 0) return `Choose the app's web listener: ${available.map((port) => `:${port}`).join(", ")}.`;
    if (!available.includes(Number(routePort))) return `Port :${routePort} is not declared by this container. Available: ${available.map((port) => `:${port}`).join(", ")}.`;
    if (available.length === 1) return `Selected the container's only declared port, :${routePort}.`;
    return `Suggested :${routePort} as the likely web listener. Other declared ports: ${available.filter((port) => port !== Number(routePort)).map((port) => `:${port}`).join(", ")}.`;
  }

  function commandFor() {
    if (editing) return `docklane route edit ${editing.id} --name ${routeName} --port ${routePort} --scheme ${routeScheme}`;
    if (!selected) return "";
    if (selected.composeProject && selected.composeService) return `docklane route add ${routeName} --project ${selected.composeProject} --service ${selected.composeService} --port ${routePort} --scheme ${routeScheme}`;
    return `docklane route add ${routeName} --container ${selected.id.slice(0, 12)} --port ${routePort} --scheme ${routeScheme}`;
  }

  function requestClose() {
    if (dirty) discardOpen = true;
    else onClose();
  }

  function handleKeydown(event: KeyboardEvent) {
    if (event.key === "Escape") requestClose();
    if (event.key !== "Tab") return;
    const drawer = event.currentTarget as HTMLElement;
    const focusable = Array.from(drawer.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled]), select:not([disabled]), [href]'));
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
  }

  async function copyCommand() {
    try {
      await navigator.clipboard.writeText(command);
      commandCopied = true;
      window.setTimeout(() => (commandCopied = false), 1800);
    } catch {
      onError("Clipboard access was unavailable");
    }
  }

  async function save() {
    if (!selected && !editing) return;
    attempted = true;
    editorError = "";
    if (issue || routePort < 1 || invalidPort) {
      if (issue) firstInput?.focus();
      return;
    }
    saving = true;
    const routeId = editing?.id;
    try {
      const body = {
        ...(editing ? { revision: editing.revision } : {}),
        name: routeName,
        selector: selected ? selectorFor(selected) : (editing as Route).selector,
        port: Number(routePort),
        scheme: routeScheme,
        enabled: editing?.enabled ?? true,
      };
      const saved = routeId ? await updateRoute(routeId, body) : await createRoute(body);
      await onSaved(`${routeId ? "Updated" : "Created"} ${routeName}.${baseDomain} · publishing route…`, saved.id || routeId || null);
    } catch (cause) {
      editorError = cause instanceof Error ? cause.message : "Route save failed";
    } finally {
      saving = false;
    }
  }
</script>

<div class="drawer-layer">
  <button class="drawer-backdrop" type="button" aria-label="Close route editor" onclick={requestClose}></button>
  <div class="route-drawer" role="dialog" aria-modal="true" aria-labelledby="route-editor-title" tabindex="-1" onkeydown={handleKeydown}>
    <div class="drawer-header"><div><p class="eyebrow">{editing ? "EDIT ROUTE" : "NEW ROUTE"}</p><h2 id="route-editor-title">{editing ? `${editing.name}.${baseDomain}` : selected?.composeService || selected?.name}</h2></div><button class="btn btn-square btn-ghost icon-button ghost" type="button" aria-label="Close route editor" onclick={requestClose}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="m6 6 12 12M18 6 6 18"></path></svg></button></div>
    <div class="drawer-workload"><span>WORKLOAD</span><strong>{selected?.composeService || selected?.name || editing?.selector.composeService || "Container"}</strong><small>{selected?.composeProject ? `${selected.composeProject} / ${selected.name}` : selected ? selected.name : "Stored route selector"}</small></div>
    <form class="drawer-form" novalidate onsubmit={(event) => { event.preventDefault(); void save(); }}>
      <label>Local hostname
        <div class="domain-input"><input class="input" bind:this={firstInput} bind:value={routeName} required maxlength="63" pattern="[a-z0-9]([a-z0-9-]*[a-z0-9])?" autocomplete="off" autocapitalize="none" spellcheck="false" aria-invalid={issue ? "true" : undefined} aria-describedby="route-name-hint" /><span>.{baseDomain}</span></div>
        {#if issue && (attempted || routeName.length > 0)}<small id="route-name-hint" class="field-hint error">{issue}</small>{:else}<small id="route-name-hint" class="field-hint">Browser URL: https://{routeName || "name"}.{baseDomain}</small>{/if}
      </label>
      <div class="field-row">
        <label>Internal port<input class="input" type="number" min="1" max="65535" list="declared-container-ports" bind:value={routePort} onchange={() => { if (!editing) routeScheme = recommendedScheme(Number(routePort)); }} aria-invalid={routePort < 1 || invalidPort ? "true" : undefined} aria-describedby="route-port-hint" required />
          {#if selected}<datalist id="declared-container-ports">{#each uniquePorts(selected.exposedPorts) as port}<option value={port}></option>{/each}</datalist>{/if}
        </label>
        <label>Protocol<select class="select" bind:value={routeScheme}><option value="http">HTTP</option><option value="https">HTTPS</option></select></label>
      </div>
      {#if selected}<small id="route-port-hint" class:error={routePort === 0 || invalidPort} class="field-hint port-hint">{portRecommendation()}</small>{:else}<small id="route-port-hint" class="field-hint port-hint">This is the protocol and port Traefik uses inside Docker. Browser access remains HTTPS.</small>{/if}
      <div class="command-preview"><div><span>EQUIVALENT CLI</span><code>{command}</code></div><button class="btn btn-square btn-ghost icon-button ghost" type="button" aria-label="Copy CLI command" title="Copy CLI command" onclick={copyCommand}>{#if commandCopied}<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m5 12 4 4L19 6"></path></svg>{:else}<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="8" y="8" width="11" height="11" rx="2"></rect><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2"></path></svg>{/if}</button></div>
      {#if editorError}<div class="alert alert-error editor-error" role="alert"><span>{editorError}</span></div>{/if}
      <div class="drawer-actions"><button type="button" class="btn btn-outline secondary" onclick={requestClose}>Cancel</button><button class="btn btn-primary" type="submit" disabled={saving || nameConflict || routePort < 1 || invalidPort} aria-busy={saving}>{#if saving}<span class="loading loading-spinner loading-sm"></span>{/if}{saving ? "Saving…" : editing ? "Save changes" : "Create route"}</button></div>
    </form>
  </div>
</div>

<ConfirmDialog open={discardOpen} title="Discard unsaved changes?" description="Your route configuration has changed. Closing now will lose those edits." confirmLabel="Discard changes" cancelLabel="Keep editing" danger onCancel={() => (discardOpen = false)} onConfirm={onClose} />
