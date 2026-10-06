import assert from "node:assert/strict";
import { existsSync, readFileSync, readdirSync } from "node:fs";
import { dirname, extname, join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

import { consumerSvg, icoExports, pngDimensions, pngExports, repoRoot, svgCopies } from "./export.mjs";

const brandRoot = dirname(fileURLToPath(import.meta.url));

const required = [
  "BRAND.md",
  "README.md",
  "LICENSES.md",
  "provenance.json",
  "tokens.json",
  "tokens.css",
  "preview.html",
  "check.mjs",
  "export.mjs",
  "logos/mark.svg",
  "logos/mark-inverse.svg",
  "logos/lockup.svg",
  "logos/lockup-inverse.svg",
  "logos/wordmark.svg",
  "logos/wordmark-inverse.svg",
  "logos/mono-black.svg",
  "logos/mono-white.svg",
  "icons/favicon.svg",
  "icons/app-icon.svg",
  "icons/icon-192.svg",
  "icons/icon-512.svg",
  "icons/maskable.svg",
  "og/og-default.svg",
  "og/og-default.png",
];

const outerPath = "M72 20 H36 C27.16 20 20 27.16 20 36 V64 C20 72.84 27.16 80 36 80 H72 V68 H38 C34.69 68 32 65.31 32 62 V38 C32 34.69 34.69 32 38 32 H72 Z";
const innerPath = "M66 38 H46 C41.58 38 38 41.58 38 46 V54 C38 58.42 41.58 62 46 62 H66 V53 H48 C46.9 53 46 52.1 46 51 V49 C46 47.9 46.9 47 48 47 H66 Z";

const markFiles = [
  "logos/mark.svg",
  "logos/mark-inverse.svg",
  "logos/lockup.svg",
  "logos/lockup-inverse.svg",
  "logos/mono-black.svg",
  "logos/mono-white.svg",
  "icons/favicon.svg",
  "icons/app-icon.svg",
  "icons/icon-192.svg",
  "icons/icon-512.svg",
  "icons/maskable.svg",
  "og/og-default.svg",
];

const wordmarkFiles = [
  "logos/lockup.svg",
  "logos/lockup-inverse.svg",
  "logos/wordmark.svg",
  "logos/wordmark-inverse.svg",
  "og/og-default.svg",
];

const expectedViewBoxes = new Map([
  ["logos/mark.svg", "0 0 100 100"],
  ["logos/mark-inverse.svg", "0 0 100 100"],
  ["logos/lockup.svg", "0 0 340 100"],
  ["logos/lockup-inverse.svg", "0 0 340 100"],
  ["logos/wordmark.svg", "0 0 230 80"],
  ["logos/wordmark-inverse.svg", "0 0 230 80"],
  ["logos/mono-black.svg", "0 0 100 100"],
  ["logos/mono-white.svg", "0 0 100 100"],
  ["icons/favicon.svg", "0 0 64 64"],
  ["icons/app-icon.svg", "0 0 512 512"],
  ["icons/icon-192.svg", "0 0 192 192"],
  ["icons/icon-512.svg", "0 0 512 512"],
  ["icons/maskable.svg", "0 0 512 512"],
  ["og/og-default.svg", "0 0 1200 630"],
]);

function walk(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);
    return entry.isDirectory() ? walk(path) : [path];
  });
}

function read(relativePath, encoding = "utf8") {
  return readFileSync(join(brandRoot, relativePath), encoding);
}

function readConsumer(relativePath, encoding = "utf8") {
  const path = join(repoRoot, relativePath);
  assert.ok(existsSync(path), `adopted consumer missing: ${relativePath}`);
  return readFileSync(path, encoding);
}

function normalizeMarkup(value) {
  return value.replace(/&amp;/g, "&").replace(/\s+/g, " ").trim();
}

function xmlAttribute(source, name) {
  const match = source.match(new RegExp(`\\b${name}=["']([^"']+)["']`, "i"));
  return match?.[1];
}

function assertSvgStructure(relativePath) {
  const source = read(relativePath);
  assert.match(source, /^<svg\b/);
  assert.equal(normalizeMarkup(xmlAttribute(source, "viewBox") ?? ""), expectedViewBoxes.get(relativePath), `${relativePath}: incorrect viewBox`);
  assert.match(source, /<title\b[^>]*>Compa<\/title>/, `${relativePath}: accessible title must be Compa`);
  assert.doesNotMatch(source, /<(?:linearGradient|radialGradient|filter)\b|\bfilter\s*=|\bglow\b/i, `${relativePath}: effects are prohibited`);
}

function assertMarkGeometry(relativePath, source = read(relativePath)) {
  assert.ok(source.includes(`d="${outerPath}"`), `${relativePath}: canonical outer path missing`);
  assert.ok(source.includes(`d="${innerPath}"`), `${relativePath}: canonical inner path missing`);
  assert.match(source, new RegExp(`<path[^>]*d="${innerPath.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}"[^>]*opacity="\\.48"`), `${relativePath}: canonical inner opacity missing`);
  assert.match(source, /<rect\s+x="72"\s+y="44"\s+width="12"\s+height="12"\s+rx="2\.5"(?:\s|\/>)/, `${relativePath}: canonical core rectangle missing`);
  assert.doesNotMatch(source, /\bstroke\s*=|\b(?:rotate|skew[XY]?)\s*\(/i, `${relativePath}: strokes, rotation, and skew are prohibited`);
}

for (const path of required) {
  assert.ok(existsSync(join(brandRoot, path)), `required file missing: ${path}`);
}

const tokens = JSON.parse(read("tokens.json"));
const provenance = JSON.parse(read("provenance.json"));
const palette = Object.fromEntries(Object.entries(tokens.color.palette).map(([name, token]) => [name, token.$value]));
assert.deepEqual(
  palette,
  {
    cobalt: "#3D63D8",
    deep: "#2749AD",
    darkSurfaceText: "#9CABFF",
    soft: "#E8ECFF",
    coreNight: "#111215",
    canvas: "#F7F5EF",
    surface: "#FFFFFF",
    ink: "#171719",
    muted: "#666976",
    line: "#D9D7D0",
  },
  "tokens.json must contain the exact approved Compa palette",
);
assert.equal(tokens.mark.fixedGeometry.outerOpenC, outerPath);
assert.equal(tokens.mark.fixedGeometry.inner, innerPath);
assert.equal(tokens.mark.fixedGeometry.innerOpacity, 0.48);
assert.deepEqual(tokens.mark.fixedGeometry.core, { element: "rect", x: 72, y: 44, width: 12, height: 12, rx: 2.5 });

assert.equal(provenance.identity.publicName, "Compa");
assert.equal(provenance.identity.visualWordmark, "compa");
assert.equal(provenance.identity.meaning, "Mexican Spanish slang for pal");
assert.equal(provenance.adoption.status, "adopted");
for (const unchanged of ["geometryChanged", "paletteChanged", "rulesChanged"]) {
  assert.equal(provenance.adoption[unchanged], false, `provenance.json: a relabel keeps ${unchanged} false`);
}
assert.deepEqual(
  provenance.compatibilityIdentifiers,
  {
    repository: "compa",
    goModule: "github.com/xibodev/compa/v2",
    binaries: ["compa", "compa-kernel"],
    stateDirectory: "~/.compa",
    matchPublicName: true,
    migrationRequired: false,
  },
  "provenance.json: the compatibility identifiers already are compa",
);

const svgFiles = walk(brandRoot)
  .map((path) => relative(brandRoot, path).split(sep).join("/"))
  .filter((path) => extname(path).toLowerCase() === ".svg");
assert.deepEqual(svgFiles.sort(), [...expectedViewBoxes.keys()].sort(), "SVG inventory differs from the expected kit");
for (const path of svgFiles) assertSvgStructure(path);
for (const path of markFiles) assertMarkGeometry(path);
for (const path of wordmarkFiles) {
  assert.match(read(path), /<text\b[^>]*>compa<\/text>/, `${path}: the wordmark must read compa`);
}

const png = read("og/og-default.png", null);
assert.ok(png.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])), "OG PNG signature is invalid");
assert.equal(png.toString("ascii", 12, 16), "IHDR", "OG PNG has no IHDR chunk in the expected position");
assert.equal(png.readUInt32BE(16), 1200, "OG PNG width must be 1200");
assert.equal(png.readUInt32BE(20), 630, "OG PNG height must be 630");

const alwaysForbiddenText = [
  { label: "the kit's earlier working name", pattern: new RegExp(`\\b${["cer", "ne"].join("")}\\b`, "gi") },
  { label: "lobster emoji", pattern: /\u{1F99E}/gu },
];
const legacyColors = ["0071e3", "0f172a", "7cc4ff", "1e2f6b", "8fd0ff", "3e5db9", "f8fafc", "64748b", "e2e8f0"].map((value) => `#${value}`);
const legacyGeometry = [["32,4", "58,22", "32,60", "6,22"].join(" "), ["M32", "4"].join(" "), ["32", "26"].join(",")];

function assertNoLegacy(path, source) {
  for (const item of alwaysForbiddenText) {
    item.pattern.lastIndex = 0;
    assert.doesNotMatch(source, item.pattern, `${path}: ${item.label} is prohibited`);
  }
  const lower = source.toLowerCase();
  for (const color of legacyColors) assert.ok(!lower.includes(color), `${path}: legacy crystal color ${color} is prohibited`);
  for (const geometry of legacyGeometry) assert.ok(!source.includes(geometry), `${path}: legacy crystal geometry is prohibited`);
}

for (const absolutePath of walk(brandRoot)) {
  const path = relative(brandRoot, absolutePath).split(sep).join("/");
  if (path === "og/og-default.png") continue;
  assertNoLegacy(path, readFileSync(absolutePath, "utf8"));
}

const preview = read("preview.html");
const references = [...preview.matchAll(/\b(?:src|href)="([^"#]+)"/g)]
  .map((match) => match[1])
  .filter((path) => !/^(?:https?:|mailto:|data:)/i.test(path));
for (const path of references) {
  const target = resolve(brandRoot, path);
  assert.ok(target.startsWith(`${brandRoot}${sep}`), `preview reference escapes brand/: ${path}`);
  assert.ok(existsSync(target), `preview reference does not exist: ${path}`);
}
assert.match(preview, /Public website header and hero/i);
assert.match(preview, /App header and sidebar, light and dark/i);
assert.match(preview, /website page not restyled/i);
assert.match(preview, /app color theme not restyled/i);

// Adopted consumers outside brand/, as provenance.json lists them.
const consumerFiles = new Set();
for (const projection of provenance.consumerProjection) {
  assert.match(projection.status, /^(?:applied|planned-not-applied)$/, `provenance.json: unknown status for ${projection.target}`);
  if (projection.status !== "applied") continue;
  assert.ok(projection.files?.length, `provenance.json: applied projection without files: ${projection.target}`);
  for (const file of projection.files) {
    readConsumer(file);
    consumerFiles.add(file);
  }
}
const exported = [...svgCopies, ...pngExports, ...icoExports].map(({ target }) => target).filter((target) => !target.startsWith("brand/"));
for (const target of exported) assert.ok(consumerFiles.has(target), `provenance.json does not list the exported ${target}`);

for (const { source, target } of svgCopies) {
  const copy = readConsumer(target);
  assert.equal(copy, consumerSvg(read(source), source), `${target} differs from brand/${source}; run node brand/export.mjs`);
  if (markFiles.includes(source)) assertMarkGeometry(target, copy);
}

for (const { target, width, height } of pngExports) {
  const size = pngDimensions(readConsumer(target, null));
  assert.deepEqual([size.width, size.height], [width, height], `${target} must be ${width}x${height}`);
}

for (const { target, entries } of icoExports) {
  const ico = readConsumer(target, null);
  assert.equal(ico.readUInt16LE(0), 0, `${target}: reserved header field must be 0`);
  assert.equal(ico.readUInt16LE(2), 1, `${target}: not an icon file`);
  assert.equal(ico.readUInt16LE(4), entries.length, `${target}: must hold ${entries.length} images`);
  let end = 6 + 16 * entries.length;
  entries.forEach(({ size }, index) => {
    const at = 6 + 16 * index;
    assert.deepEqual([ico[at] || 256, ico[at + 1] || 256], [size, size], `${target}: entry ${index} must be ${size}px`);
    assert.equal(ico.readUInt16LE(at + 4), 1, `${target} ${size}px: one color plane`);
    assert.equal(ico.readUInt16LE(at + 6), 32, `${target} ${size}px: 32 bits per pixel`);
    const length = ico.readUInt32LE(at + 8);
    const offset = ico.readUInt32LE(at + 12);
    assert.equal(offset, end, `${target} ${size}px: images must be stored in directory order`);
    const image = pngDimensions(ico.subarray(offset, offset + length));
    assert.deepEqual([image.width, image.height], [size, size], `${target} ${size}px: PNG size differs from its directory entry`);
    assert.equal(image.colorType, 6, `${target} ${size}px: ICO images must be RGBA PNG`);
    end = offset + length;
  });
  assert.equal(end, ico.length, `${target}: unexpected bytes after the last image`);
}

const manifestPath = "web/frontend/public/site.webmanifest";
const manifest = JSON.parse(readConsumer(manifestPath));
assert.equal(manifest.name, "Compa", `${manifestPath}: name`);
assert.equal(manifest.short_name, "Compa", `${manifestPath}: short_name`);
assert.equal(manifest.theme_color, palette.cobalt, `${manifestPath}: theme_color must be Cobalt`);
assert.equal(manifest.background_color, palette.coreNight, `${manifestPath}: background_color must be Core Night`);
for (const { src } of manifest.icons) {
  assert.ok(consumerFiles.has(`web/frontend/public${src}`), `${manifestPath}: icon ${src} is not an adopted kit file`);
}

const indexPath = "web/frontend/index.html";
const index = readConsumer(indexPath);
assert.match(index, /<title>Compa<\/title>/, `${indexPath}: title`);
assert.match(index, new RegExp(`<meta name="theme-color" content="${palette.cobalt}"`), `${indexPath}: theme-color must match the manifest`);
const iconLinks = [...index.matchAll(/<link rel="(?:icon|apple-touch-icon)"[^>]*href="([^"]+)"/g)].map((match) => match[1]);
assert.ok(iconLinks.length > 0, `${indexPath}: no icon links`);
for (const href of iconLinks) {
  assert.ok(consumerFiles.has(`web/frontend/public${href}`), `${indexPath}: icon ${href} is not an adopted kit file`);
}

const logoPath = "web/frontend/src/components/brand-logo.tsx";
const logo = readConsumer(logoPath);
assert.ok(logo.includes(`"${outerPath}"`), `${logoPath}: canonical outer path missing`);
assert.ok(logo.includes(`"${innerPath}"`), `${logoPath}: canonical inner path missing`);
assert.match(logo, /x=\{72\}\s+y=\{44\}\s+width=\{12\}\s+height=\{12\}\s+rx=\{2\.5\}/, `${logoPath}: canonical core rectangle missing`);
assert.match(logo, /opacity=\{0?\.48\}/, `${logoPath}: canonical inner opacity missing`);
for (const color of [palette.cobalt, palette.deep, palette.darkSurfaceText, palette.soft, palette.ink]) {
  assert.ok(logo.includes(color), `${logoPath}: kit color ${color} missing`);
}

for (const file of consumerFiles) {
  if (/\.(?:svg|html|webmanifest|tsx)$/.test(file)) assertNoLegacy(file, readConsumer(file));
}

console.log(
  `Compa brand check passed: ${required.length} required files, ${svgFiles.length} SVGs, JSON, references, geometry, naming, 1200x630 PNG, and ${consumerFiles.size} adopted consumer files.`,
);
