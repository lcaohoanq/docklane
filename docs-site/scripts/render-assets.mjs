import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import sharp from "sharp";

const source = new URL("../public/social-card.svg", import.meta.url);
const target = new URL("../public/social-card.png", import.meta.url);
const svg = await readFile(source);

await sharp(svg)
  .png({ compressionLevel: 9 })
  .toFile(fileURLToPath(target));
