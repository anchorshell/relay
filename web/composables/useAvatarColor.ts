const avatarPalette = [
  ['#c5221f', '#ffffff'], ['#d93025', '#ffffff'], ['#e67c73', '#06182c'], ['#f4511e', '#ffffff'],
  ['#f6bf26', '#06182c'], ['#7cb342', '#06182c'], ['#0b8043', '#ffffff'], ['#33b679', '#06182c'],
  ['#039be5', '#ffffff'], ['#4285f4', '#ffffff'], ['#3f51b5', '#ffffff'], ['#7986cb', '#06182c'],
  ['#9e69af', '#ffffff'], ['#8e24aa', '#ffffff'], ['#ad1457', '#ffffff'], ['#616161', '#ffffff'],
  ['#5f6368', '#ffffff'], ['#00838f', '#ffffff'], ['#00796b', '#ffffff'], ['#6d4c41', '#ffffff']
] as const

export function avatarInitialFor(value: string) {
  const normalized = String(value || 'A').normalize('NFKC').trim()
  return normalized.match(/[\p{L}\p{N}]/u)?.[0]?.toUpperCase() || 'A'
}

export function avatarStyleFor(value: string) {
  const normalized = String(value || 'A').normalize('NFKC').trim().toUpperCase() || 'A'
  const hash = [...normalized].reduce((total, character) => ((total * 31) + character.charCodeAt(0)) >>> 0, 0)
  const [backgroundColor, color] = avatarPalette[hash % avatarPalette.length]
  return { backgroundColor, color, boxShadow: 'none' }
}
