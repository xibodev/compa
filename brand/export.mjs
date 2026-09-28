// Rebuilds every Compa raster, ICO file, and consumer SVG copy from the SVG
// masters in this directory:
//
//   node brand/export.mjs
//
// It renders with the Chromium that Playwright installs for the frontend, so it
// needs only Node built-ins and web/frontend/node_modules/@playwright/test.
// Each master opens as its own document in a viewport of exactly the target
// size at device scale factor 1 and is captured as a PNG. The same Chromium
// build and fonts give the same bytes; the SHA-256 printed for every file makes
// that easy to confirm. check.mjs imports the plan below to verify the outputs.

import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const brandRoot = dirname(fileURLToPath(import.meta.url));
export const repoRoot = resolve(brandRoot, "..");

/** Consumer SVGs: a master plus the intrinsic width and height of its viewBox. */
export const svgCopies = [
  { source: "icons/favicon.svg", target: "web/frontend/public/favicon.svg" },
  { source: "logos/mark.svg", target: "web/frontend/public/compa-mark.svg" },
  { source: "logos/lockup.svg", target: "web/frontend/public/compa-logo.svg" },
  { source: "logos/mark.svg", target: "docs/compa-mark.svg" },
  { source: "logos/lockup.svg", target: "docs/compa-logo.svg" },
];

/** PNG rasters. `transparent` keeps the corners outside a rounded tile clear. */
export const pngExports = [
  { source: "og/og-default.svg", target: "brand/og/og-default.png", width: 1200, height: 630, transparent: false },
  { source: "icons/favicon.svg", target: "web/frontend/public/favicon-96x96.png", width: 96, height: 96, transparent: true },
  // iOS rounds a touch icon itself and paints transparency black, so this one
  // is the full-bleed maskable tile.
  { source: "icons/maskable.svg", target: "web/frontend/public/apple-touch-icon.png", width: 180, height: 180, transparent: false },
  { source: "icons/icon-192.svg", target: "web/frontend/public/web-app-manifest-192x192.png", width: 192, height: 192, transparent: true },
  { source: "icons/icon-512.svg", target: "web/frontend/public/web-app-manifest-512x512.png", width: 512, height: 512, transparent: true },
  // The tray icon on systems other than Windows, at the size it always had.
  { source: "icons/app-icon.svg", target: "web/backend/icon.png", width: 512, height: 512, transparent: true },
];

// Tabs, taskbars, and trays can be light or dark, so every small size is the
// favicon tile: the inverse mark on Core Night reads on both.
const tile = (size) => ({ size, source: "icons/favicon.svg" });

/** ICO files: one PNG-compressed entry per size, smallest first. */
export const icoExports = [
  { target: "web/frontend/public/favicon.ico", entries: [16, 32, 48].map(tile) },
  // Windows embeds this for the tray; it loads the 32, 48, or 64 entry for the
  // display scale and shrinks it to the notification area.
  {
    target: "web/backend/icon.ico",
    entries: [...[16, 24, 32, 48, 64].map(tile), { size: 256, source: "icons/app-icon.svg" }],
  },
];

const PNG_SIGNATURE = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);

/** Width and height from a PNG's IHDR chunk. */
export function pngDimensions(bytes) {
  if (!bytes.subarray(0, 8).equals(PNG_SIGNATURE) || bytes.toString("ascii", 12, 16) !== "IHDR") {
    throw new Error("not a PNG with a leading IHDR chunk");
  }
  return { width: bytes.readUInt32BE(16), height: bytes.readUInt32BE(20), colorType: bytes[25] };
}

/** The consumer copy of an SVG master. */
export function consumerSvg(masterSource, masterPath) {
  const viewBox = masterSource.match(/\bviewBox="([^"]+)"/)?.[1];
  if (!viewBox) throw new Error(`${masterPath}: no viewBox`);
  const [, , width, height] = viewBox.trim().split(/\s+/);
  return masterSource
    .replace(/\bviewBox="[^"]+"/, `viewBox="${viewBox}" width="${width}" height="${height}"`)
    .replace(
      /(<title\b[^>]*>[^<]*<\/title>)/,
      `$1\n  <!-- Generated from brand/${masterPath} by brand/export.mjs. Edit the master, then rebuild. -->`,
    );
}

/** An ICO file whose entries are the given PNGs, in order. */
export function packIco(images) {
  const header = Buffer.alloc(6);
  header.writeUInt16LE(0, 0); // reserved
  header.writeUInt16LE(1, 2); // 1 = icon
  header.writeUInt16LE(images.length, 4);
  const directory = Buffer.alloc(16 * images.length);
  let offset = header.length + directory.length;
  images.forEach(({ size, png }, index) => {
    const at = 16 * index;
    directory.writeUInt8(size >= 256 ? 0 : size, at); // 0 means 256
    directory.writeUInt8(size >= 256 ? 0 : size, at + 1);
    directory.writeUInt8(0, at + 2); // no palette
    directory.writeUInt8(0, at + 3); // reserved
    directory.writeUInt16LE(1, at + 4); // color planes
    directory.writeUInt16LE(32, at + 6); // bits per pixel
    directory.writeUInt32LE(png.length, at + 8);
    directory.writeUInt32LE(offset, at + 12);
    offset += png.length;
  });
  return Buffer.concat([header, directory, ...images.map(({ png }) => png)]);
}

const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");

async function main() {
  const requireFromFrontend = createRequire(join(repoRoot, "web", "frontend", "package.json"));
  const { chromium } = requireFromFrontend("@playwright/test");

  const written = [];
  const write = (target, bytes) => {
    writeFileSync(join(repoRoot, target), bytes);
    written.push({ target, bytes });
  };

  for (const { source, target } of svgCopies) {
    write(target, consumerSvg(readFileSync(join(brandRoot, source), "utf8"), source));
  }

  // Greyscale text antialiasing: subpixel (LCD) antialiasing would bake color
  // fringes, tuned to one monitor's pixel layout, into the OG card's text.
  const browser = await chromium.launch({ headless: true, args: ["--disable-lcd-text"] });
  try {
    const context = await browser.newContext({ deviceScaleFactor: 1 });
    const page = await context.newPage();
    const render = async (source, width, height, transparent) => {
      await page.setViewportSize({ width, height });
      await page.goto(pathToFileURL(join(brandRoot, source)).href, { waitUntil: "load" });
      await page.evaluate(() => document.fonts?.ready.then(() => true));
      const png = await page.screenshot({ type: "png", omitBackground: transparent });
      const size = pngDimensions(png);
      if (size.width !== width || size.height !== height) {
        throw new Error(`${source}: rendered ${size.width}x${size.height}, expected ${width}x${height}`);
      }
      return png;
    };

    for (const { source, target, width, height, transparent } of pngExports) {
      write(target, await render(source, width, height, transparent));
    }
    for (const { target, entries } of icoExports) {
      const images = [];
      for (const { size, source } of entries) images.push({ size, png: await render(source, size, size, true) });
      write(target, packIco(images));
    }
  } finally {
    await browser.close();
  }

  for (const { target, bytes } of written) console.log(`${sha256(bytes)}  ${target}`);
}

const invokedPath = process.argv[1] ? resolve(process.argv[1]) : "";
const ownPath = fileURLToPath(import.meta.url);
const invokedDirectly = process.platform === "win32"
  ? invokedPath.toLowerCase() === ownPath.toLowerCase()
  : invokedPath === ownPath;
if (invokedDirectly) await main();
