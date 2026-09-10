import React from 'react'

// Minimal Markdown renderer for announcements. Emits React nodes directly —
// no HTML strings, no dangerouslySetInnerHTML — so untrusted-looking input
// can never inject markup. Supported syntax: # headings, **bold**, *italic*,
// `code`, ```fenced code blocks```, [links](https://...), - / 1. lists,
// > blockquotes and --- rules; everything else renders as plain text.

const INLINE_RE = /(`[^`\n]+`)|(\*\*[^*\n]+\*\*)|(\*[^*\n]+\*)|(\[[^\]\n]+\]\((?:https?:\/\/)[^)\s]+\))/g

function renderInline(text:string):React.ReactNode[] {
  const out:React.ReactNode[] = []
  let last = 0, key = 0
  for (const match of text.matchAll(INLINE_RE)) {
    const idx = match.index ?? 0
    if (idx > last) out.push(text.slice(last, idx))
    const token = match[0]
    if (match[1]) out.push(<code key={key++}>{token.slice(1, -1)}</code>)
    else if (match[2]) out.push(<strong key={key++}>{renderInline(token.slice(2, -2))}</strong>)
    else if (match[3]) out.push(<em key={key++}>{renderInline(token.slice(1, -1))}</em>)
    else {
      const close = token.indexOf('](')
      out.push(<a key={key++} href={token.slice(close + 2, -1)} target="_blank" rel="noreferrer">{token.slice(1, close)}</a>)
    }
    last = idx + token.length
  }
  if (last < text.length) out.push(text.slice(last))
  return out
}

// Paragraph lines keep their soft line breaks (announcements are often
// written with plain newlines rather than blank-line paragraphs).
function renderLines(lines:string[]):React.ReactNode[] {
  return lines.flatMap((line, i) => i ? [<br key={`br${i}`}/>, ...renderInline(line)] : renderInline(line))
}

export function Markdown({source}:{source:string}) {
  const lines = source.replace(/\r\n/g, '\n').split('\n')
  const blocks:React.ReactNode[] = []
  let key = 0
  let i = 0
  while (i < lines.length) {
    const line = lines[i]
    if (!line.trim()) { i++; continue }
    if (line.trimStart().startsWith('```')) {
      const code:string[] = []
      i++
      while (i < lines.length && !lines[i].trimStart().startsWith('```')) code.push(lines[i++])
      i++ // closing fence (or end of input)
      blocks.push(<pre key={key++}><code>{code.join('\n')}</code></pre>)
      continue
    }
    const heading = /^(#{1,6})\s+(.*)$/.exec(line)
    if (heading) {
      const Tag = `h${Math.min(heading[1].length + 2, 6)}` as 'h3'|'h4'|'h5'|'h6'
      blocks.push(<Tag key={key++}>{renderInline(heading[2])}</Tag>)
      i++
      continue
    }
    if (/^\s*(---+|\*\*\*+)\s*$/.test(line)) { blocks.push(<hr key={key++}/>); i++; continue }
    if (line.trimStart().startsWith('>')) {
      const quoted:string[] = []
      while (i < lines.length && lines[i].trimStart().startsWith('>')) quoted.push(lines[i++].replace(/^\s*>\s?/, ''))
      blocks.push(<blockquote key={key++}>{renderLines(quoted)}</blockquote>)
      continue
    }
    const unordered = /^\s*[-*+]\s+/, ordered = /^\s*\d+[.)]\s+/
    if (unordered.test(line) || ordered.test(line)) {
      const isOrdered = ordered.test(line)
      const marker = isOrdered ? ordered : unordered
      const items:React.ReactNode[] = []
      while (i < lines.length && marker.test(lines[i])) items.push(<li key={items.length}>{renderInline(lines[i++].replace(marker, ''))}</li>)
      blocks.push(isOrdered ? <ol key={key++}>{items}</ol> : <ul key={key++}>{items}</ul>)
      continue
    }
    const para:string[] = []
    while (i < lines.length && lines[i].trim() && !/^(#{1,6}\s|\s*[-*+]\s|\s*\d+[.)]\s|\s*>|\s*```|\s*(---+|\*\*\*+)\s*$)/.test(lines[i])) para.push(lines[i++])
    blocks.push(<p key={key++}>{renderLines(para)}</p>)
  }
  return <div className="markdown">{blocks}</div>
}
