const allModelRelayPermissions = [
  'relay:request',
  'relay:usage:read:self',
  'relay:usage:read:organization',
  'relay:limits:read:self',
  'relay:limits:read:organization',
  'relay:limits:manage',
  'relay:providers:read',
  'relay:providers:manage',
  'relay:groups:read',
  'relay:groups:manage',
  'relay:queue:read:self',
  'relay:queue:read:organization',
  'relay:queue:manage',
  'relay:logs:read:self',
  'relay:logs:read:organization',
  'relay:settings:view',
  'relay:settings:manage'
]

export function useModelRelayPermissions() {
  const permissions = computed(() => allModelRelayPermissions)
  const permissionSet = computed(() => new Set(permissions.value))
  const has = (permission: string) => permissionSet.value.has(permission)
  const hasAny = (items: string[]) => items.some(permission => has(permission))

  return {
    permissions,
    has,
    hasAny
  }
}
