export function useFormatters() {
  const relative = useRelativeTime()

  function number(value?: number | null) {
    return new Intl.NumberFormat('en-US').format(value ?? 0)
  }

  function currencyMicros(value?: number | null, currency = 'USD') {
    return new Intl.NumberFormat('en-US', {
      style: 'currency',
      currency,
      maximumFractionDigits: 4
    }).format((value ?? 0) / 1_000_000)
  }

  function microsToDollars(value?: number | null) {
    return (value ?? 0) / 1_000_000
  }

  function microsToDollarInput(value?: number | null) {
    if (!value) return ''
    return microsToDollars(value).toFixed(6).replace(/\.?0+$/, '')
  }

  function dollarsToMicros(value?: string | number | null) {
    if (value == null || value === '') return 0
    const parsed = typeof value === 'number' ? value : Number(String(value).trim())
    if (!Number.isFinite(parsed) || parsed <= 0) return 0
    return Math.round(parsed * 1_000_000)
  }

  function durationMs(value?: number | null) {
    const ms = Math.max(0, Math.floor(value ?? 0))
    if (ms < 60_000) {
      return `${(ms / 1000).toFixed(1)}s`
    }

    let totalSeconds = Math.floor(ms / 1000)
    const days = Math.floor(totalSeconds / 86_400)
    totalSeconds -= days * 86_400
    const hours = Math.floor(totalSeconds / 3_600)
    totalSeconds -= hours * 3_600
    const minutes = Math.floor(totalSeconds / 60)
    totalSeconds -= minutes * 60
    const seconds = totalSeconds

    if (days > 0) {
      return hours > 0 ? `${days}d ${hours}h` : `${days}d`
    }
    if (hours > 0) {
      return minutes > 0 ? `${hours}h ${minutes}m` : `${hours}h`
    }
    if (minutes > 0) {
      return seconds > 0 ? `${minutes}m ${seconds}s` : `${minutes}m`
    }
    return `${Math.max(1, seconds)}s`
  }

  function countdownMs(value?: number | null) {
    const ms = Math.max(0, value ?? 0)
    if (ms < 1000) {
      return `${(ms / 1000).toFixed(1)}s`
    }
    if (ms < 60_000) {
      return `${(ms / 1000).toFixed(1)}s`
    }

    const totalSeconds = Math.floor(ms / 1000)
    const days = Math.floor(totalSeconds / 86_400)
    const hours = Math.floor((totalSeconds % 86_400) / 3_600)
    const minutes = Math.floor((totalSeconds % 3_600) / 60)
    const seconds = totalSeconds % 60

    if (days > 0) {
      if (hours > 0) return `${days}d ${hours}h`
      return `${days}d`
    }
    if (hours > 0) {
      if (minutes > 0) return `${hours}h ${minutes}m`
      return `${hours}h`
    }
    if (minutes > 0) {
      if (seconds > 0) return `${minutes}m ${seconds}s`
      return `${minutes}m`
    }
    return `${(ms / 1000).toFixed(1)}s`
  }

  function dateTime(value?: string | null) {
    if (!value) return 'Never'
    return new Date(value).toLocaleString()
  }

  function clockTime(value?: string | null) {
    if (!value) return 'Never'
    return new Date(value).toLocaleTimeString('en-US', {
      hour: 'numeric',
      minute: '2-digit',
      second: '2-digit'
    })
  }

  function relativeTime(value?: string | null) {
    if (!value) return 'Never'
    return relative.format(value)
  }

  function healthTone(status?: string) {
    switch (status) {
      case 'healthy':
        return 'emerald'
      case 'cooling_down':
        return 'sky'
      case 'rate_limited':
        return 'amber'
      case 'unhealthy':
        return 'rose'
      default:
        return 'slate'
    }
  }

  return { number, currencyMicros, microsToDollars, microsToDollarInput, dollarsToMicros, durationMs, countdownMs, dateTime, clockTime, relativeTime, healthTone }
}
