import type { RequestCharacterization, RequestLog } from '~/types/admin'

export type RequestTimingBreakdown = {
  queueWaitMS: number
  characterizationMS: number | null
  guardrailPreMS: number | null
  providerLatencyMS: number | null
  guardrailPostMS: number | null
  totalMS: number | null
}

export function requestTimingBreakdown(log?: RequestLog | null): RequestTimingBreakdown {
  if (!log) {
    return {
      queueWaitMS: 0,
      characterizationMS: null,
      guardrailPreMS: null,
      providerLatencyMS: null,
      guardrailPostMS: null,
      totalMS: null
    }
  }

  const characterization = parsedCharacterization(log.characterization_json)
  const characterizationMS = characterization?.classifier_status === 'pending' ? null : typeof characterization?.classification_duration_ms === 'number'
    ? Math.max(0, characterization.classification_duration_ms)
    : optionalDuration(log.characterization_duration_ms)
  const guardrailPreMS = optionalDuration(log.guardrail_pre_duration_ms)
  const guardrailPostMS = optionalDuration(log.guardrail_post_duration_ms)
  const relayLatencyMS = optionalDuration(log.latency_ms)
  const explicitProviderLatencyMS = optionalDuration(log.provider_latency_ms)
  const providerWasDispatched = String(log.guardrail_status || '').toLowerCase() !== 'blocked_pre'
  const providerLatencyMS = providerWasDispatched
    ? explicitProviderLatencyMS ?? (relayLatencyMS == null
        ? null
        : Math.max(0, relayLatencyMS - (guardrailPreMS || 0) - (guardrailPostMS || 0)))
    : null
  const queueWaitMS = Math.max(0, Number(log.wait_ms || 0))
  const calculatedTotalMS = queueWaitMS
    + (characterization?.classification_background ? 0 : (characterizationMS || 0))
    + (guardrailPreMS || 0)
    + (providerLatencyMS || 0)
    + (guardrailPostMS || 0)

  return {
    queueWaitMS,
    characterizationMS,
    guardrailPreMS,
    providerLatencyMS,
    guardrailPostMS,
    totalMS: optionalDuration(log.total_time_ms)
      ?? (calculatedTotalMS > 0 ? calculatedTotalMS : null)
  }
}

export function formatRequestTiming(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return '—'
  const milliseconds = Math.max(0, value)
  const seconds = milliseconds / 1000
  if (milliseconds > 0 && seconds < 0.001) return '<0.001s'
  if (seconds < 1) return `${seconds.toFixed(3)}s`
  if (seconds < 100) return `${seconds.toFixed(1)}s`
  return `${Math.round(seconds)}s`
}

function parsedCharacterization(raw?: string | null): RequestCharacterization | null {
  if (!raw) return null
  try {
    return JSON.parse(raw) as RequestCharacterization
  } catch {
    return null
  }
}

function optionalDuration(value?: number | null): number | null {
  const milliseconds = Number(value || 0)
  return Number.isFinite(milliseconds) && milliseconds > 0 ? milliseconds : null
}
