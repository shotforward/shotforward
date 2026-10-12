import { Fragment, type JSX, type ReactNode } from 'react';

interface InlineToken {
  index: number;
  length: number;
  render: (key: string) => ReactNode;
}

export function AgentMarkdown({ text }: { text: string }) {
  return <div className="agent-markdown">{renderBlocks(text.replaceAll('\r\n', '\n'))}</div>;
}

function renderBlocks(source: string, keyPrefix = 'block'): ReactNode[] {
  const lines = source.split('\n');
  const result: ReactNode[] = [];
  let index = 0;

  while (index < lines.length) {
    const line = lines[index] ?? '';
    if (!line.trim()) {
      index += 1;
      continue;
    }

    const fence = line.match(/^\s*```([\w+-]*)\s*$/);
    if (fence) {
      const body: string[] = [];
      index += 1;
      while (index < lines.length && !/^\s*```\s*$/.test(lines[index] ?? '')) {
        body.push(lines[index] ?? '');
        index += 1;
      }
      if (index < lines.length) index += 1;
      const language = fence[1] ? `language-${fence[1]}` : undefined;
      result.push(
        <pre key={`${keyPrefix}-${result.length}`}>
          <code className={language}>{body.join('\n')}</code>
        </pre>,
      );
      continue;
    }

    const heading = line.match(/^(#{1,6})\s+(.+)$/);
    if (heading) {
      const Heading = `h${(heading[1] ?? '').length}` as keyof JSX.IntrinsicElements;
      result.push(<Heading key={`${keyPrefix}-${result.length}`}>{renderInline(heading[2] ?? '')}</Heading>);
      index += 1;
      continue;
    }

    if (/^\s*(?:-{3,}|\*{3,}|_{3,})\s*$/.test(line)) {
      result.push(<hr key={`${keyPrefix}-${result.length}`} />);
      index += 1;
      continue;
    }

    if (/^\s*>/.test(line)) {
      const quote: string[] = [];
      while (index < lines.length) {
        const match = (lines[index] ?? '').match(/^\s*>\s?(.*)$/);
        if (!match) break;
        quote.push(match[1] ?? '');
        index += 1;
      }
      result.push(
        <blockquote key={`${keyPrefix}-${result.length}`}>
          {renderBlocks(quote.join('\n'), `${keyPrefix}-quote`)}
        </blockquote>,
      );
      continue;
    }

    if (isTableHeader(lines, index)) {
      const headers = splitTableRow(lines[index] ?? '');
      index += 2;
      const rows: string[][] = [];
      while (index < lines.length && (lines[index] ?? '').includes('|') && (lines[index] ?? '').trim()) {
        rows.push(splitTableRow(lines[index] ?? ''));
        index += 1;
      }
      result.push(
        <table key={`${keyPrefix}-${result.length}`}>
          <thead>
            <tr>
              {headers.map((cell, cellIndex) => (
                <th key={cellIndex}>{renderInline(cell)}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row, rowIndex) => (
              <tr key={rowIndex}>
                {headers.map((_, cellIndex) => (
                  <td key={cellIndex}>{renderInline(row[cellIndex] ?? '')}</td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>,
      );
      continue;
    }

    const unordered = line.match(/^\s*[-+*]\s+(.+)$/);
    if (unordered) {
      const items: string[] = [];
      while (index < lines.length) {
        const match = (lines[index] ?? '').match(/^\s*[-+*]\s+(.+)$/);
        if (!match) break;
        items.push(match[1] ?? '');
        index += 1;
      }
      result.push(
        <ul key={`${keyPrefix}-${result.length}`}>
          {items.map((item, itemIndex) => (
            <ListItem key={itemIndex} source={item} />
          ))}
        </ul>,
      );
      continue;
    }

    const ordered = line.match(/^\s*\d+[.)]\s+(.+)$/);
    if (ordered) {
      const items: string[] = [];
      while (index < lines.length) {
        const match = (lines[index] ?? '').match(/^\s*\d+[.)]\s+(.+)$/);
        if (!match) break;
        items.push(match[1] ?? '');
        index += 1;
      }
      result.push(
        <ol key={`${keyPrefix}-${result.length}`}>
          {items.map((item, itemIndex) => (
            <li key={itemIndex}>{renderInline(item)}</li>
          ))}
        </ol>,
      );
      continue;
    }

    const paragraph = [line];
    index += 1;
    while (index < lines.length && (lines[index] ?? '').trim() && !startsBlock(lines, index)) {
      paragraph.push(lines[index] ?? '');
      index += 1;
    }
    result.push(
      <p key={`${keyPrefix}-${result.length}`}>
        {paragraph.map((value, lineIndex) => (
          <Fragment key={lineIndex}>
            {lineIndex > 0 && <br />}
            {renderInline(value)}
          </Fragment>
        ))}
      </p>,
    );
  }

  return result;
}

function ListItem({ source }: { source: string }) {
  const task = source.match(/^\[([ xX])]\s*(.*)$/);
  return (
    <li>
      {task && <input type="checkbox" checked={(task[1] ?? '').toLowerCase() === 'x'} disabled readOnly />}
      {renderInline(task ? (task[2] ?? '') : source)}
    </li>
  );
}

function startsBlock(lines: string[], index: number): boolean {
  const line = lines[index] ?? '';
  return (
    /^\s*```/.test(line) ||
    /^(#{1,6})\s+/.test(line) ||
    /^\s*(?:-{3,}|\*{3,}|_{3,})\s*$/.test(line) ||
    /^\s*>/.test(line) ||
    /^\s*[-+*]\s+/.test(line) ||
    /^\s*\d+[.)]\s+/.test(line) ||
    isTableHeader(lines, index)
  );
}

function isTableHeader(lines: string[], index: number): boolean {
  return (
    index + 1 < lines.length &&
    (lines[index] ?? '').includes('|') &&
    /^\s*\|?\s*:?-{3,}:?\s*(?:\|\s*:?-{3,}:?\s*)+\|?\s*$/.test(lines[index + 1] ?? '')
  );
}

function splitTableRow(source: string): string[] {
  const value = source.trim().replace(/^\|/, '').replace(/\|$/, '');
  const cells: string[] = [];
  let cell = '';
  let escaped = false;
  for (const character of value) {
    if (escaped) {
      cell += character;
      escaped = false;
    } else if (character === '\\') {
      escaped = true;
    } else if (character === '|') {
      cells.push(cell.trim());
      cell = '';
    } else {
      cell += character;
    }
  }
  cells.push(cell.trim());
  return cells;
}

function renderInline(source: string): ReactNode[] {
  const result: ReactNode[] = [];
  let remaining = source;
  let key = 0;

  while (remaining) {
    const token = firstInlineToken(remaining);
    if (!token) {
      result.push(remaining);
      break;
    }
    if (token.index > 0) result.push(remaining.slice(0, token.index));
    result.push(token.render(`inline-${key}`));
    remaining = remaining.slice(token.index + token.length);
    key += 1;
  }
  return result;
}

function firstInlineToken(source: string): InlineToken | undefined {
  const candidates: InlineToken[] = [];
  addInlineCandidate(candidates, source, /`([^`\n]+)`/, (match, tokenKey) => (
    <code key={tokenKey}>{match[1] ?? ''}</code>
  ));
  addInlineCandidate(
    candidates,
    source,
    /!\[([^\]]*)]\(((?:[^()\s]+|\([^()]*\))+)(?:\s+"[^"]*")?\)/,
    (match, tokenKey) => (
      <span className="agent-markdown__image-label" key={tokenKey}>
        {match[1] ? `[图片：${match[1]}]` : '[图片]'}
      </span>
    ),
  );
  addInlineCandidate(
    candidates,
    source,
    /\[([^\]]+)]\(((?:[^()\s]+|\([^()]*\))+)(?:\s+"[^"]*")?\)/,
    (match, tokenKey) => {
      const href = safeLink(match[2] ?? '');
      return href ? (
        <a
          href={href}
          target={isExternalLink(href) ? '_blank' : undefined}
          rel="noreferrer noopener"
          key={tokenKey}
        >
          {renderInline(match[1] ?? '')}
        </a>
      ) : (
        <Fragment key={tokenKey}>{renderInline(match[1] ?? '')}</Fragment>
      );
    },
  );
  addInlineCandidate(candidates, source, /\*\*([^*]+)\*\*|__([^_]+)__/, (match, tokenKey) => (
    <strong key={tokenKey}>{renderInline(match[1] ?? match[2] ?? '')}</strong>
  ));
  addInlineCandidate(candidates, source, /~~([^~]+)~~/, (match, tokenKey) => (
    <del key={tokenKey}>{renderInline(match[1] ?? '')}</del>
  ));
  addInlineCandidate(candidates, source, /\*([^*\n]+)\*|_([^_\n]+)_/, (match, tokenKey) => (
    <em key={tokenKey}>{renderInline(match[1] ?? match[2] ?? '')}</em>
  ));
  return candidates.sort((left, right) => left.index - right.index || right.length - left.length)[0];
}

function addInlineCandidate(
  candidates: InlineToken[],
  source: string,
  pattern: RegExp,
  render: (match: RegExpMatchArray, key: string) => ReactNode,
) {
  const match = source.match(pattern);
  if (match?.index === undefined || !match[0]) return;
  candidates.push({
    index: match.index,
    length: match[0].length,
    render: (key) => render(match, key),
  });
}

function safeLink(value: string): string | undefined {
  const link = value.trim();
  if (!link || /[\u0000-\u001f\u007f]/.test(link) || link.startsWith('//')) return undefined;
  if (/^(?:https?:|mailto:)/i.test(link)) return link;
  if (/^[a-z][a-z\d+.-]*:/i.test(link)) return undefined;
  return link;
}

function isExternalLink(value: string): boolean {
  return /^(?:https?:|mailto:)/i.test(value);
}
