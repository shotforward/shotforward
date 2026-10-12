import { readFileSync, readdirSync } from 'node:fs';
import { extname } from 'node:path';

const sourceRoot = new URL('../src/', import.meta.url);
const tokenFile = new URL('styles/tokens.css', sourceRoot);
const maximumRawColorOccurrences = 655;
const maximumUniqueRawColors = 481;
const retiredBrandColors = new Set([
  '#006aff',
  '#0d5dcc',
  '#145fc7',
  '#1769d2',
  '#1769e0',
  '#2563eb',
  '#2864dc',
  '#2878d0',
]);

function collectStyleFiles(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const url = new URL(`${entry.name}${entry.isDirectory() ? '/' : ''}`, directory);
    if (entry.isDirectory()) return collectStyleFiles(url);
    return extname(entry.name) === '.css' && url.href !== tokenFile.href ? [url] : [];
  });
}

const files = collectStyleFiles(sourceRoot);
const colors = files.flatMap((file) =>
  [...readFileSync(file, 'utf8').matchAll(/#[0-9a-fA-F]{3,8}\b/g)].map((match) => ({
    color: match[0].toLowerCase(),
    file,
  })),
);
const uniqueColors = new Set(colors.map(({ color }) => color));
const retiredUsages = colors.filter(({ color }) => retiredBrandColors.has(color));
const failures = [];

if (colors.length > maximumRawColorOccurrences) {
  failures.push(
    `raw color occurrences grew to ${colors.length} (migration budget ${maximumRawColorOccurrences})`,
  );
}
if (uniqueColors.size > maximumUniqueRawColors) {
  failures.push(
    `unique raw colors grew to ${uniqueColors.size} (migration budget ${maximumUniqueRawColors})`,
  );
}
if (retiredUsages.length) {
  const sample = retiredUsages
    .slice(0, 5)
    .map(({ color, file }) => `${color} in ${file.pathname.replace(sourceRoot.pathname, 'src/')}`)
    .join(', ');
  failures.push(`retired bright-blue brand colors returned: ${sample}`);
}

console.log(
  `Style tokens: ${files.length} stylesheets; ${colors.length} raw color occurrences; ${uniqueColors.size} unique raw colors.`,
);
if (failures.length) {
  for (const failure of failures) console.error(failure);
  process.exitCode = 1;
}
