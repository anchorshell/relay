export type RequestRouteLabels = {
  provider: string
  model: string
}

export function explicitProviderModelTarget(value?: string | null): RequestRouteLabels | null {
  const target = String(value || '').trim()
  const delimiter = target.indexOf('/')
  if (delimiter <= 0 || delimiter >= target.length - 1) return null

  const provider = target.slice(0, delimiter).trim()
  const model = target.slice(delimiter + 1).trim()
  if (!provider || !model) return null
  return { provider, model }
}

export function requestRouteLabels(
  providerName?: string | null,
  incomingModel?: string | null,
  selectedModel?: string | null,
  fallbackModel = 'No upstream selected'
): RequestRouteLabels {
  const provider = String(providerName || '').trim()
  const selected = String(selectedModel || '').trim()
  const requested = provider && provider !== 'Provider missing'
    ? null
    : explicitProviderModelTarget(incomingModel)

  return {
    provider: requested?.provider || provider || 'Provider missing',
    model: selected || requested?.model || fallbackModel
  }
}

export function recentModelUsageLabels(providerName?: string | null, modelName?: string | null): RequestRouteLabels {
  const provider = String(providerName || '').trim()
  const model = String(modelName || '').trim()
  const requested = provider && provider !== 'Provider missing'
    ? null
    : explicitProviderModelTarget(model)

  return {
    provider: requested?.provider || provider || 'Provider missing',
    model: requested?.model || model || 'Unknown model'
  }
}
