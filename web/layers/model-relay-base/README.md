<!-- @format -->

# Model Relay Base Nuxt Layer

This layer is the shared visual-system seam for downstream private Model Relay builds.

Keep reusable theme behavior, visual tokens, and non-product-specific shell components here as they are extracted from the public app. The public `web` app remains standalone, and private wrappers should extend the public app/layer instead of copying the full UI tree.

Nuxt layers override same-path pages; they do not merge them. For no-drift extensions, public pages should expose reusable `Base*Page` components with neutral named slots. Downstream builds may override routes only as thin slot-filling wrappers around those base pages. Public OSS UI must not reference private-product concepts.

## Page Extension Migration Pattern

The Usage page is the reference implementation for future OSS/pro UI migrations.

When a downstream build needs to extend a public page without drifting from the OSS page:

1. Move the public page's real implementation into a shared base component under this layer.
   - Example: `components/usage/BaseUsagePage.vue`.
   - The base component owns data loading, filters, cards, tables, loading/error states, empty states, and the public visual design.
   - Keep this component neutral. It must not mention Pro, Premium, private repos, Team Members, billing plans, or downstream-only product concepts.

2. Keep the public route thin.
   - Example: `relay/web/pages/usage.vue` renders only `<BaseUsagePage />`.
   - The OSS route should look and behave the same when no slots are supplied.

3. Expose named slots from the base component for extension.
   - Use neutral names such as `summary-extra`, `filters-extra`, `table-before`, `row-actor`, `empty-extra`, and `table-after`.
   - Pass only safe page data as slot props.
   - Do not pass secrets, raw request/response bodies, provider credentials, admin tokens, or signed downstream context.
   - Render optional columns or sections only when the slot exists, so the OSS page has no empty gaps.

4. Add a root-level alias component when the file is nested.
   - Nuxt auto-imports `components/usage/BaseUsagePage.vue` as `UsageBaseUsagePage`, not `BaseUsagePage`.
   - To support clean route wrappers, create `components/BaseUsagePage.vue` that wraps `UsageBaseUsagePage` and forwards slots conditionally.

Use this pattern before adding any same-path page override for dashboard, realtime, queue, logs, limits, or settings. Full page overrides are reserved for intentionally different pages, and even then they should reuse shared base components where practical.

## Interactive cursors

The public app's `app.config.ts` owns Nuxt UI action-slot cursors; downstream Nuxt layers inherit it. Shared `UiButton` and `UiSwitch` primitives own their button cursors. `assets/css/interactions.css` covers explicit native control and shell classes and can be imported independently by consumers with their own theme stylesheet.

Use pointer cursors only for enabled click targets. Preserve disabled, drag, and text-input cursors; do not apply pointers to generic cards, table rows, icons, or tooltip-only labels. Native one-off buttons need their own cursor utilities when they do not use a shared control class.
