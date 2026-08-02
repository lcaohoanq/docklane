import type { Container } from "./types";

export function uniquePorts(ports: number[]) {
  return [...new Set(ports.filter((port) => port > 0))].sort((left, right) => left - right);
}

export function filterContainers(containers: Container[], search: string) {
  const query = search.trim().toLowerCase();
  if (!query) return containers;
  return containers.filter((container) =>
    [
      container.name,
      container.image,
      container.status,
      container.composeProject || "",
      container.composeService || "",
      ...container.exposedPorts.map(String),
    ].some((value) => value.toLowerCase().includes(query)),
  );
}

export function groupContainers(containers: Container[]) {
  return [
    {
      id: "routeable-containers",
      title: "Available for routing",
      description: "Running workloads with a declared internal TCP port.",
      containers: containers.filter((container) => container.routeEligibility.eligible),
    },
    {
      id: "read-only-containers",
      title: "Read-only containers",
      description: "System workloads and containers unavailable for HTTP routing.",
      containers: containers.filter((container) => !container.routeEligibility.eligible),
    },
  ];
}
