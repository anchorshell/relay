export type LimitProgressBand = 'low' | 'medium' | 'high' | 'critical' | 'full'

export function limitProgressBand(percent: number, forcedFull = false): LimitProgressBand {
  const normalized = Math.max(0, Math.min(100, Number(percent) || 0))
  if (forcedFull || normalized >= 100) return 'full'
  if (normalized >= 90) return 'critical'
  if (normalized >= 60) return 'high'
  if (normalized >= 30) return 'medium'
  return 'low'
}
