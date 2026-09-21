export function useRelativeTime() {
  const formatter = new Intl.RelativeTimeFormat('en', { numeric: 'auto' })

  function format(value: string | Date) {
    const target = typeof value === 'string' ? new Date(value).getTime() : value.getTime()
    const diff = target - Date.now()
    const units: Array<[Intl.RelativeTimeFormatUnit, number]> = [
      ['day', 86_400_000],
      ['hour', 3_600_000],
      ['minute', 60_000],
      ['second', 1000]
    ]
    for (const [unit, size] of units) {
      if (Math.abs(diff) >= size || unit === 'second') {
        return formatter.format(Math.round(diff / size), unit)
      }
    }
    return 'now'
  }

  return { format }
}
