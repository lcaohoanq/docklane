import { expect, test, type Page } from "@playwright/test";

const containers = [
  {
    id: "abc123",
    name: "budget-app-1",
    image: "example/budget:latest",
    status: "Up 8 minutes",
    composeProject: "budget",
    composeService: "app",
    exposedPorts: [3000],
  },
  {
    id: "gateway123",
    name: "docklane",
    image: "docklane:local",
    status: "Up 10 minutes",
    systemRole: "reverse-proxy",
    exposedPorts: [4646],
  },
];

const routes = [
  {
    id: 1,
    revision: 2,
    name: "budget",
    selector: { composeProject: "budget", composeService: "app" },
    port: 3000,
    scheme: "http",
    enabled: true,
    observed: {
      state: "ready",
      containerName: "budget-app-1",
      upstreamUrl: "http://budget-app-1:3000",
    },
  },
];

async function mockAPI(page: Page) {
  await page.route("**/api/v1/**", async (request) => {
    const url = new URL(request.request().url());
    if (url.pathname === "/api/v1/containers") {
      return request.fulfill({ json: { containers } });
    }
    if (url.pathname === "/api/v1/routes") {
      return request.fulfill({
        json: {
          routes,
          baseDomain: "docker.home.arpa",
          reconcileIntervalMs: 5000,
        },
      });
    }
    if (url.pathname === "/api/v1/routes/1/readiness") {
      return request.fulfill({
        json: {
          routeId: 1,
          revision: 2,
          state: "ready",
          ready: true,
          message: "Route is active.",
          checkedAt: new Date().toISOString(),
        },
      });
    }
    return request.fulfill({ status: 404, json: { error: "Not mocked" } });
  });
}

test.beforeEach(async ({ page }) => {
  await mockAPI(page);
  await page.goto("/");
});

test("opens on the operational Routes view", async ({ page }) => {
  await expect(page.getByRole("heading", { name: "Routes", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /Home/ })).toHaveCount(0);
  await expect(page.getByLabel("Route status summary")).toContainText("Ready");
  await expect(page.getByText("budget.docker.home.arpa", { exact: true })).toBeVisible();
});

test("uses custom route actions and delete confirmation", async ({ page }) => {
  await page.getByRole("button", { name: "More actions for budget" }).click();
  await page.getByRole("button", { name: "Delete route" }).click();

  const dialog = page.getByRole("alertdialog");
  await expect(dialog).toContainText("budget.docker.home.arpa");
  await expect(dialog).toContainText("The container is not changed");
  await expect(dialog.getByRole("button", { name: "Cancel" })).toBeFocused();
});

test("creates routes from a mobile container card", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: /Containers/ }).click();
  await page.getByRole("button", { name: "Create route" }).first().click();

  const drawer = page.getByRole("dialog", { name: /app/i });
  await expect(drawer).toBeVisible();
  await expect(drawer.getByLabel("Local hostname")).toBeFocused();
  await drawer.getByLabel("Local hostname").fill("budget-new");
  await drawer.getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByRole("dialog", { name: "Discard unsaved changes?" })).toBeVisible();

  const layout = await page.evaluate(() => ({
    overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    targets: [...document.querySelectorAll<HTMLButtonElement>("button")]
      .filter((button) => button.getBoundingClientRect().width > 0)
      .filter((button) => !button.classList.contains("modal-backdrop"))
      .filter((button) => !button.classList.contains("drawer-backdrop"))
      .map((button) => button.getBoundingClientRect().height),
  }));
  expect(layout.overflow).toBe(0);
  expect(Math.min(...layout.targets)).toBeGreaterThanOrEqual(44);
});
