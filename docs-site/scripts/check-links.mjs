import { access, readFile, readdir } from "node:fs/promises";
import { extname, join } from "node:path";
import { fileURLToPath } from "node:url";

const dist = new URL("../dist/", import.meta.url);
const distPath = fileURLToPath(dist);
const base = "/docklane";
const failures = [];

async function walk(directory) {
  const entries = await readdir(directory, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) files.push(...(await walk(path)));
    if (entry.isFile() && entry.name.endsWith(".html")) files.push(path);
  }
  return files;
}

function localTarget(value) {
  if (
    !value ||
    value.startsWith("#") ||
    value.startsWith("http:") ||
    value.startsWith("https:") ||
    value.startsWith("mailto:") ||
    value.startsWith("data:")
  ) {
    return undefined;
  }

  const pathname = decodeURIComponent(value.split("#", 1)[0].split("?", 1)[0]);
  if (!pathname.startsWith(base)) return undefined;
  const relative = pathname.slice(base.length).replace(/^\/+/, "");
  if (!relative) return "index.html";
  if (relative.endsWith("/")) return `${relative}index.html`;
  if (extname(relative)) return relative;
  return `${relative}/index.html`;
}

for (const htmlFile of await walk(distPath)) {
  const html = await readFile(htmlFile, "utf8");
  for (const match of html.matchAll(/\b(?:href|src)="([^"]+)"/g)) {
    const target = localTarget(match[1]);
    if (!target) continue;
    try {
      await access(new URL(target, dist));
    } catch {
      failures.push(`${htmlFile}: ${match[1]}`);
    }
  }
}

if (failures.length > 0) {
  throw new Error(`Broken local links:\n${failures.join("\n")}`);
}

console.log("Verified generated local links.");
