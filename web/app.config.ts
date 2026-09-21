// Nuxt layers inherit these cursor-only variants. Keep the library's disabled
// states intact; do not turn generic panels, labels, or tooltip targets into actions.
export default defineAppConfig({
  ui: {
    button: { slots: { base: 'cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed' } },
    dropdownMenu: { slots: { item: 'cursor-pointer data-disabled:cursor-not-allowed' } },
    selectMenu: { slots: {
      base: 'cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed',
      item: 'cursor-pointer data-disabled:cursor-not-allowed'
    } },
    tabs: { slots: { trigger: 'cursor-pointer disabled:cursor-not-allowed data-disabled:cursor-not-allowed' } },
    switch: { slots: { base: 'cursor-pointer disabled:cursor-not-allowed data-disabled:cursor-not-allowed', label: 'cursor-pointer' } },
    calendar: { slots: { cellTrigger: 'cursor-pointer data-disabled:cursor-not-allowed data-unavailable:cursor-not-allowed' } },
    stepper: { slots: { trigger: 'cursor-pointer disabled:cursor-not-allowed aria-disabled:cursor-not-allowed data-disabled:cursor-not-allowed' } }
  }
})
