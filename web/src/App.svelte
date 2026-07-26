<script lang="ts">
  import { onMount } from "svelte";

  type Container = {
    id: string;
    name: string;
    image: string;
    status: string;
    composeProject?: string;
    composeService?: string;
    exposedPorts: number[];
  };

  type Selector = {
    composeProject: string;
    composeService: string;
    containerId?: string;
  };

  type Observation = {
    state: "ready" | "disabled" | "unresolved" | "ambiguous" | "error";
    message?: string;
    containerName?: string;
    upstreamUrl?: string;
    checkedAt?: string;
  };

  type Route = {
    id: number;
    name: string;
    selector: Selector;
    port: number;
    scheme: string;
    enabled: boolean;
    observed: Observation;
  };

  let containers: Container[] = [];
  let routes: Route[] = [];
  let baseDomain = "docker.home.arpa";
  let reconcileEverySeconds = 5;
  let loading = true;
  let error = "";
  let notice = "";
  let selected: Container | null = null;
  let editing: Route | null = null;
  let routeName = "";
  let routePort = 80;
  let routeScheme = "http";
  let saving = false;

  async function refresh(showLoading = true) {
    if (showLoading) loading = true;
    try {
      const [containersResponse, routesResponse] = await Promise.all([
        fetch("/api/v1/containers"),
        fetch("/api/v1/routes"),
      ]);
      if (!containersResponse.ok)
        throw new Error(`Discovery failed (${containersResponse.status})`);
      if (!routesResponse.ok)
        throw new Error(`Route loading failed (${routesResponse.status})`);
      const containersPayload = await containersResponse.json();
      const routesPayload = await routesResponse.json();
      containers = containersPayload.containers;
      routes = routesPayload.routes;
      baseDomain = routesPayload.baseDomain;
      reconcileEverySeconds = Math.max(
        1,
        Math.round((routesPayload.reconcileIntervalMs || 5000) / 1000),
      );
      if (!showLoading) error = "";
    } catch (cause) {
      error = cause instanceof Error ? cause.message : "Refresh failed";
    } finally {
      if (showLoading) loading = false;
    }
  }

  function choose(container: Container) {
    selected = container;
    editing = null;
    routeName = slug(container.composeService || container.name);
    routePort = container.exposedPorts[0] || 80;
    routeScheme = "http";
    notice = "";
  }

  function edit(route: Route) {
    editing = route;
    selected =
      containers.find((container) => matches(route.selector, container)) || null;
    routeName = route.name;
    routePort = route.port;
    routeScheme = route.scheme;
    notice = "";
  }

  function closeEditor() {
    selected = null;
    editing = null;
  }

  function matches(selector: Selector, container: Container) {
    if (selector.containerId)
      return container.id.startsWith(selector.containerId);
    return (
      selector.composeProject === container.composeProject &&
      selector.composeService === container.composeService
    );
  }

  function slug(value: string) {
    return value
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-|-$/g, "");
  }

  function selectorFor(container: Container): Selector {
    if (container.composeProject && container.composeService) {
      return {
        composeProject: container.composeProject,
        composeService: container.composeService,
      };
    }
    return {
      composeProject: "",
      composeService: "",
      containerId: container.id,
    };
  }

  function commandFor() {
    if (editing) {
      return `docklane route edit ${editing.id} --name ${routeName} --port ${routePort} --scheme ${routeScheme}`;
    }
    if (!selected) return "";
    if (selected.composeProject && selected.composeService) {
      return `docklane route add ${routeName} --project ${selected.composeProject} --service ${selected.composeService} --port ${routePort} --scheme ${routeScheme}`;
    }
    return `docklane route add ${routeName} --container ${selected.id.slice(0, 12)} --port ${routePort} --scheme ${routeScheme}`;
  }

  function writableRoute(route: Route, enabled = route.enabled) {
    return {
      name: route.name,
      selector: route.selector,
      port: route.port,
      scheme: route.scheme,
      enabled,
    };
  }

  async function saveRoute() {
    if (!selected && !editing) return;
    saving = true;
    error = "";
    notice = "";
    const selector = selected
      ? selectorFor(selected)
      : (editing as Route).selector;
    const routeId = editing?.id;
    try {
      const response = await fetch(
        routeId ? `/api/v1/routes/${routeId}` : "/api/v1/routes",
        {
          method: routeId ? "PUT" : "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            name: routeName,
            selector,
            port: Number(routePort),
            scheme: routeScheme,
            enabled: editing?.enabled ?? true,
          }),
        },
      );
      const payload = await response.json();
      if (!response.ok)
        throw new Error(payload.error || `Save failed (${response.status})`);
      notice = `${routeId ? "Updated" : "Created"} https://${routeName}.${baseDomain}`;
      closeEditor();
      await refresh(false);
    } catch (cause) {
      error = cause instanceof Error ? cause.message : "Route save failed";
    } finally {
      saving = false;
    }
  }

  async function toggle(route: Route) {
    error = "";
    const response = await fetch(`/api/v1/routes/${route.id}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(writableRoute(route, !route.enabled)),
    });
    const payload = await response.json();
    if (!response.ok) {
      error = payload.error || `Update failed (${response.status})`;
      return;
    }
    notice = `${route.enabled ? "Disabled" : "Enabled"} ${route.name}`;
    await refresh(false);
  }

  async function remove(route: Route) {
    if (!confirm(`Delete the route ${route.name}.${baseDomain}?`)) return;
    error = "";
    const response = await fetch(`/api/v1/routes/${route.id}`, {
      method: "DELETE",
    });
    if (!response.ok) {
      const payload = await response.json();
      error = payload.error || `Delete failed (${response.status})`;
      return;
    }
    if (editing?.id === route.id) closeEditor();
    notice = `Deleted ${route.name}.${baseDomain}`;
    await refresh(false);
  }

  onMount(() => {
    refresh();
    const timer = window.setInterval(() => refresh(false), 5000);
    return () => window.clearInterval(timer);
  });
</script>

<svelte:head>
  <title>Docklane · Local container gateway</title>
</svelte:head>

<main>
  <header>
    <div>
      <p class="eyebrow">LOCAL CONTAINER GATEWAY</p>
      <h1>Your containers deserve names, not port numbers.</h1>
      <p class="lede">
        Discover HTTP services, assign a local domain, and let Traefik handle
        the route.
      </p>
    </div>
    <button onclick={() => refresh()}>Refresh Docker</button>
  </header>

  {#if notice}
    <div class="notice">{notice}</div>
  {/if}
  {#if error}
    <div class="notice error">{error}</div>
  {/if}

  {#if selected || editing}
    <section class="route-editor" aria-labelledby="route-editor-title">
      <div>
        <p class="eyebrow">{editing ? "EDIT ROUTE" : "NEW ROUTE"}</p>
        <h2 id="route-editor-title">
          {editing?.name || selected?.composeService || selected?.name}
        </h2>
        <p>
          Configure the workload's internal listener. No application host port
          is required.
        </p>
      </div>
      <form
        onsubmit={(event) => {
          event.preventDefault();
          saveRoute();
        }}
      >
        <label>
          Local name
          <div class="domain-input">
            <input
              bind:value={routeName}
              required
              pattern="[a-z0-9]([a-z0-9-]*[a-z0-9])?"
            />
            <span>.{baseDomain}</span>
          </div>
        </label>
        <div class="field-row">
          <label>
            Container port
            <input
              type="number"
              min="1"
              max="65535"
              bind:value={routePort}
              required
            />
          </label>
          <label>
            Upstream scheme
            <select bind:value={routeScheme}>
              <option value="http">HTTP</option>
              <option value="https">HTTPS</option>
            </select>
          </label>
        </div>
        <div class="form-actions">
          <button type="submit" disabled={saving}>
            {saving ? "Saving…" : editing ? "Save changes" : "Create route"}
          </button>
          <button type="button" class="secondary" onclick={closeEditor}>
            Cancel
          </button>
        </div>
        <code>{commandFor()}</code>
      </form>
    </section>
  {/if}

  <section aria-labelledby="routes-title">
    <div class="section-title">
      <h2 id="routes-title">Routes</h2>
      <span>{routes.length} saved · reconciles every {reconcileEverySeconds}s</span>
    </div>
    {#if routes.length === 0}
      <div class="empty compact">No routes yet. Choose a container below.</div>
    {:else}
      <div class="route-list">
        {#each routes as route}
          <div class="route-row">
            <span
              class={`route-state ${route.observed?.state || "error"}`}
              title={route.observed?.message || "Waiting for reconciliation"}
            ></span>
            <div class="route-name">
              <a href={`https://${route.name}.${baseDomain}`} target="_blank">
                {route.name}.{baseDomain}
              </a>
              <small>
                {route.observed?.state || "pending"}
                {#if route.observed?.containerName}
                  · {route.observed.containerName}
                {/if}
              </small>
            </div>
            <code>{route.scheme} · :{route.port}</code>
            <div class="route-actions">
              <button class="ghost small" onclick={() => edit(route)}>Edit</button>
              <button class="ghost small" onclick={() => toggle(route)}>
                {route.enabled ? "Disable" : "Enable"}
              </button>
              <button class="ghost danger small" onclick={() => remove(route)}>
                Delete
              </button>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  </section>

  <section aria-labelledby="containers-title">
    <div class="section-title">
      <h2 id="containers-title">Containers</h2>
      <span>{containers.length} discovered</span>
    </div>

    {#if loading}
      <div class="empty">Inspecting Docker…</div>
    {:else if containers.length === 0}
      <div class="empty">No running containers found.</div>
    {:else}
      <div class="grid">
        {#each containers as container}
          <article>
            <div class="status-dot" title={container.status}></div>
            <div class="card-body">
              <h3>{container.composeService || container.name}</h3>
              <p>{container.image}</p>
              <div class="meta">
                {#each container.exposedPorts as port}
                  <span>:{port}</span>
                {:else}
                  <span>no declared HTTP port</span>
                {/each}
              </div>
            </div>
            <button class="secondary" onclick={() => choose(container)}>
              Create route
            </button>
          </article>
        {/each}
      </div>
    {/if}
  </section>
</main>
