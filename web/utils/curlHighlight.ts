export type CurlSyntaxTone = 'plain' | 'command' | 'flag' | 'method' | 'string' | 'variable' | 'key' | 'operator'
export type CurlSyntaxToken = { text: string, tone: CurlSyntaxTone }

// A small lexer for our generated curl examples, not a general shell parser.
// Read whole shell words (including quotes and escaped apostrophes) before
// coloring anything inside them. Quote context must survive JSON line breaks.
export function highlightCurl(source: string): CurlSyntaxToken[][] {
  const lines: CurlSyntaxToken[][] = [[]]
  const append = (text: string, tone: CurlSyntaxTone = 'plain') => {
    text.split('\n').forEach((part, index) => {
      if (index) lines.push([])
      if (part) lines[lines.length - 1]!.push({ text: part, tone })
    })
  }
  const json = (word: string) => {
    let offset = 0
    for (const match of word.matchAll(/"(?:\\.|[^"\\])*"/g)) {
      append(word.slice(offset, match.index))
      offset = match.index + match[0].length
      append(match[0], /^\s*:/.test(word.slice(offset)) ? 'key' : 'string')
    }
    append(word.slice(offset))
  }
  let offset = 0
  let dataArgument = false
  const words = /(?:[^\s'"\\]|\\[\s\S]|'[^']*'|"(?:\\[\s\S]|[^"\\])*")+/g
  for (const match of source.matchAll(words)) {
    append(source.slice(offset, match.index))
    const word = match[0]
    offset = match.index + word.length
    if (/^\\(?:\r?\n)?$/.test(word)) {
      append(word, 'operator')
      continue
    }
    if (dataArgument) {
      json(word)
      dataArgument = false
    } else if (word.startsWith('"')) {
      // Only double-quoted shell words expand variables. User-entered tokens
      // are single-quoted; a literal ${...} inside one is not a substitution.
      for (const part of word.split(/(\$\{[A-Z0-9_]+\})/g)) {
        append(part, part.startsWith('${') ? 'variable' : 'string')
      }
    } else {
      const tone: CurlSyntaxTone = word === 'curl' ? 'command'
        : word === 'POST' ? 'method'
          : word.startsWith('-') ? 'flag'
            : word.startsWith("'") ? 'string' : 'plain'
      append(word, tone)
      dataArgument = word === '--data-raw'
    }
  }
  append(source.slice(offset))
  return lines
}
