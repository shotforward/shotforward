import { describe, expect, it } from 'vitest';
import { parseScriptOutline } from './ScriptWorkspacePage';

describe('parseScriptOutline', () => {
  it('keeps only level-two and level-three headings with stable line numbers', () => {
    expect(
      parseScriptOutline('# Title\n\n## Scene one\nBody\n### Beat A\n#### Ignored\n## Scene two'),
    ).toEqual([
      { depth: 2, title: 'Scene one', line: 3 },
      { depth: 3, title: 'Beat A', line: 5 },
      { depth: 2, title: 'Scene two', line: 7 },
    ]);
  });

  it('does not treat malformed headings as outline entries', () => {
    expect(parseScriptOutline('##\n###    \ntext ## inline')).toEqual([]);
  });
});
