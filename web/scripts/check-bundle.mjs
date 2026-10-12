import { readFileSync, readdirSync, statSync } from 'node:fs';
import { gzipSync } from 'node:zlib';
import { basename } from 'node:path';

const output = new URL('../../daemon/internal/webui/dist/', import.meta.url);
const assets = new URL('assets/', output);
const index = readFileSync(new URL('index.html', output), 'utf8');
const initialFiles = [...index.matchAll(/(?:src|href)="\/(assets\/[^"]+\.(?:js|css))"/g)].map(
  (match) => match[1],
);
const files = readdirSync(assets).filter((file) => /\.(?:js|css)$/.test(file));
const measurements = new Map(
  files.map((file) => {
    const content = readFileSync(new URL(file, assets));
    return [file, { raw: statSync(new URL(file, assets)).size, gzip: gzipSync(content).byteLength }];
  }),
);

const kib = (bytes) => bytes / 1024;
const initialGzip = initialFiles.reduce(
  (total, file) => total + (measurements.get(basename(file))?.gzip ?? 0),
  0,
);
const entry = files.find((file) => /^index-[\w-]+\.js$/.test(file));
const stylesheet = files.find((file) => /^index-[\w-]+\.css$/.test(file));
const lazyChunks = files.filter(
  (file) => file.endsWith('.js') && !initialFiles.some((path) => basename(path) === file),
);
const largestLazy = lazyChunks.reduce(
  (largest, file) => (measurements.get(file).gzip > (measurements.get(largest)?.gzip ?? 0) ? file : largest),
  '',
);

const limits = [
  ['initial JavaScript and CSS', initialGzip, 180],
  ['application entry', measurements.get(entry)?.gzip ?? Infinity, 90],
  ['global stylesheet', measurements.get(stylesheet)?.gzip ?? Infinity, 20],
  ['largest lazy route', measurements.get(largestLazy)?.gzip ?? 0, 22.1],
];
const failures = limits.filter(([, bytes, limit]) => kib(bytes) > limit);
console.log(
  `Bundle budget: initial ${kib(initialGzip).toFixed(1)} KiB gzip; entry ${kib(measurements.get(entry)?.gzip ?? 0).toFixed(1)} KiB; CSS ${kib(measurements.get(stylesheet)?.gzip ?? 0).toFixed(1)} KiB; largest lazy ${largestLazy || 'none'} ${kib(measurements.get(largestLazy)?.gzip ?? 0).toFixed(1)} KiB.`,
);
if (failures.length) {
  for (const [label, bytes, limit] of failures)
    console.error(`${label} is ${kib(bytes).toFixed(1)} KiB gzip (limit ${limit} KiB).`);
  process.exitCode = 1;
}
