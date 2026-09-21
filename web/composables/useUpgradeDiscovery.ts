import type { InjectionKey } from 'vue'
import type { PaidFeatureID } from '../utils/paidFeatures'

export type UpgradeDiscovery = {
  open: (feature: PaidFeatureID, trigger: HTMLElement) => void
}

export const upgradeDiscoveryKey: InjectionKey<UpgradeDiscovery> = Symbol('relay-upgrade-discovery')

// Opt-in through the standalone root, not a global store or inferred edition.
// An inheriting app that replaces app.vue gets no promotional UI by default.
export function useUpgradeDiscovery(): UpgradeDiscovery | null {
  return inject(upgradeDiscoveryKey, null)
}
