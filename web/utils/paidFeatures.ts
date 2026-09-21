/** Hosted-product discovery only, not a paid entitlement or permission check. */
export const DEFAULT_RELAY_UPGRADE_URL = 'https://anchorshell.com/pricing'

const offer = {
  availability: 'Available in hosted AnchorShell Relay. Hosted Relay includes a free tier; feature availability depends on your plan.',
  ctaLabel: 'Explore hosted Relay'
} as const

export const paidFeatures = {
  enhancedClassification: {
    ...offer,
    title: 'AnchorShell Classifier Pro + Enhanced (proprietary)',
    actionLabel: 'Explore enhanced classification',
    headline: 'Understand the work behind each request.',
    description: 'Hosted Relay adds stronger request classification. Combine it with Smart Groups to route by intent, domain, complexity, and your configured model assignments.',
    ossNote: 'AnchorShell Classifier Basic and locally hosted Laya remain available in Community Edition.',
    ctaLabel: 'Get started with hosted Relay',
    icon: ['M9 3h6v4H9z', 'M4 17h6v4H4z', 'M14 17h6v4h-6z', 'M12 7v5H7v5', 'M12 12h5v5']
  },
  smartGroups: {
    ...offer,
    title: 'Smart Groups',
    actionLabel: 'Add Smart Group',
    headline: 'Match the model to the request.',
    description: 'Use request characterization to choose models for different tasks, with automatic assignments, configurable preferences, and ranked fallbacks.',
    ossNote: 'Standard groups and manually ranked fallbacks remain part of open-source Relay.',
    icon: ['M6 7a2 2 0 1 0 0-4 2 2 0 0 0 0 4Z', 'M18 21a2 2 0 1 0 0-4 2 2 0 0 0 0 4Z', 'M8 5h8a4 4 0 0 1 0 8H8a3 3 0 0 0 0 6h8']
  },
  teams: {
    ...offer,
    title: 'Teams',
    actionLabel: 'Teams',
    headline: 'Build with your team.',
    description: 'Invite teammates into one AnchorShell workspace. Give each person their own account while working with shared Relay configuration.',
    ossNote: 'Your standalone Relay remains fully usable without an AnchorShell account.',
    icon: ['M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z', 'M2 21v-2a4 4 0 0 1 4-4h6a4 4 0 0 1 4 4v2', 'M16 3a4 4 0 0 1 0 8', 'M22 21v-2a4 4 0 0 0-3-3.87']
  },
  permissions: {
    ...offer,
    title: 'Granular Permissions',
    actionLabel: 'Team Permissions',
    headline: 'Control who can do what.',
    description: 'Separate request access from provider, group, and settings management. Decide who can see their own activity and who can inspect team-wide usage, logs, queues, and limits.',
    ossNote: 'Open-source Relay already separates management authentication from inference access.',
    icon: ['M12 3.5l7 3v5.2c0 4.2-2.8 7.3-7 8.8-4.2-1.5-7-4.6-7-8.8V6.5z', 'M9 12l2 2 4-4']
  },
  apiKeys: {
    ...offer,
    title: 'Managed API Keys',
    actionLabel: 'Managed API Keys',
    navLabel: 'API Keys',
    headline: 'Give every integration its own key.',
    description: 'Create individually attributable API keys owned by users, use separate keys for different applications, and revoke one without replacing everyone’s credentials.',
    ossNote: 'RELAY_API_TOKEN remains the deployment-wide inference credential in open-source Relay.',
    icon: ['M15.5 13a5.5 5.5 0 1 0-4.96-3.12L3 17.5V21h3.5l2-2V16.5H11l2-2', 'M16.5 6.5h.01']
  },
  budgets: {
    ...offer,
    title: 'User & API Key Budgets',
    actionLabel: 'User & API key budgets',
    headline: 'Set individual usage limits.',
    description: 'Set request, token, and spending limits for individual users and API keys, with time windows and provider or model targets. One exhausted budget need not block the whole team.',
    ossNote: 'Deployment, provider, and resource limits remain available in open-source Relay.',
    icon: ['M20.2 16.2a9 9 0 1 0-16.4 0', 'M12 12l4-4', 'M12 14a2 2 0 1 0 0-4 2 2 0 0 0 0 4Z', 'M5 19h14']
  },
  usageAttribution: {
    ...offer,
    title: 'Usage Attribution',
    actionLabel: 'By user or API key',
    headline: 'Know who’s using what.',
    description: 'Attribute requests, tokens, and costs to individual users and API keys. Filter team activity to understand which people and integrations are consuming capacity.',
    ossNote: 'Deployment-wide usage, cost tracking, and provider/model analytics remain part of open-source Relay.',
    icon: ['M5 19V5', 'M5 19h14', 'M8 16v-4', 'M12 16V8', 'M16 16v-7']
  },
  connectedProviders: {
    ...offer,
    title: 'Connected Subscription Accounts',
    actionLabel: 'Add Subscription Plan',
    headline: 'Put supported subscription access to work.',
    description: 'Connect an OpenAI Codex subscription account, discover available models, and manage who can use the connection. Support is provider-specific, not a promise that every subscription can be connected.',
    ossNote: 'Ordinary API providers and encrypted provider credentials remain available in open-source Relay.',
    icon: ['M9 3v4', 'M15 3v4', 'M7 7h10v4a5 5 0 0 1-10 0Z', 'M12 16v5']
  },
  classificationFeedback: {
    ...offer,
    title: 'Classification Feedback',
    actionLabel: 'Review classification',
    headline: 'Understand and review classifications.',
    description: 'Record corrections to request classifications for offline review and evaluation. Feedback does not automatically retrain the model or change live routing.',
    ossNote: 'Basic request characterization and its recorded results remain part of open-source Relay.',
    icon: ['M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2Z', 'M7 7h10', 'M7 11h6']
  }
} as const

export type PaidFeatureID = keyof typeof paidFeatures
export type PaidFeature = (typeof paidFeatures)[PaidFeatureID]

export function paidFeatureLabel(id: PaidFeatureID, navigation = false): string {
  const feature = paidFeatures[id]
  return navigation && 'navLabel' in feature ? feature.navLabel : feature.actionLabel
}

/** Only public, credential-free HTTPS destinations are suitable for this link. */
export function relayUpgradeURL(value: unknown): string {
  try {
    const url = new URL(typeof value === 'string' ? value : '')
    if (url.protocol === 'https:' && !url.username && !url.password) return url.href
  } catch { /* Invalid or absent configuration uses the confirmed site route. */ }
  return DEFAULT_RELAY_UPGRADE_URL
}
