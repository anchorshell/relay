// Standalone fallback, matching internal/config.DefaultHTTPAddr. Development
// overrides are supplied by make dev through the existing Nuxt target settings.
export const DEFAULT_RELAY_PORT = 11730
export const DEFAULT_RELAY_ORIGIN = `http://localhost:${DEFAULT_RELAY_PORT}`
