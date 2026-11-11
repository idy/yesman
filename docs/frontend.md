# Frontend integration

This is the proposed frontend contract for yesman. No application or UI implementation is present yet.

## Stack and ownership

Use a Tauri 2 WebView with TypeScript and HTML. Use [idy/magpie-design-system](https://github.com/idy/magpie-design-system) for tokens, base elements, components, shell styles, fonts, and icons. Start without an additional UI framework; a frontend bundler can compile TypeScript and package local assets without changing Magpie's buildless distribution.

The frontend owns presentation, selection, drafts, and cached views. The Rust host service owns task state, authorization, scheduling, memory, and persistence. The Tauri Rust bridge connects the frontend to local and configured LAN hosts.

## Reference and packaging

The inspected Magpie reference is **v0.4.3**, commit [`46ba098239979506a4ccc14721aac044483c66bc`](https://github.com/idy/magpie-design-system/commit/46ba098239979506a4ccc14721aac044483c66bc), checked on 2026-11-11.

Package the pinned assets with the app. Preserve the sibling `css/`, `js/`, `icons/`, and `fonts/` directories so relative imports and asset references continue to work. Do not load the UI's styles, fonts, or icons from a runtime CDN.

| Surface | Upstream entry or convention |
| --- | --- |
| Complete stylesheet | `css/magpie.css` |
| Stylesheet imports | `css/tokens.css`, `css/base.css`, `css/components.css`, `css/layout.css` |
| Font loading | Tokens load `css/fonts.css`, which references `../fonts/` |
| JavaScript helpers | `js/magpie.js`; helpers initialize only when called |
| Icon sprite | `icons/icons.svg`; local external SVG `use` references |
| CSS naming | `mds-` component classes and `--mds-` tokens |
| Theme | System light/dark by default; explicit `data-theme` or `setTheme` overrides |

Retain the upstream root `LICENSE`, `css/Magpie-MIT.txt`, `icons/LICENSE`, and the Montserrat, Monda, and Lato OFL license files under `fonts/`. Copying only the asset directories would omit the root license.

## Application structure

- **Navigation:** connected hosts and their permanent leads; an all-hosts board is a separate view.
- **Task board:** the selected lead's tasks or an aggregation across selected hosts. Cards retain host, lead, goal, repository, and execution-agent identity.
- **Lead conversation:** direct discussion of a goal, triage item, or delivery question with the selected lead.
- **Task details:** acceptance criteria, dependencies, findings, execution attempts, and delivery evidence.
- **Human triage:** a clearly accessible **to triage** column and item actions that record the user's disposition.

Changing the selected lead must also change the conversation target. An aggregate board must not silently send a message to whichever lead happened to be selected previously. A disconnected host remains identifiable as stale and cannot accept a new command through a local optimistic UI update.

## Applying Magpie

Reuse `.mds-shell`, `.mds-sidebar`, `.mds-nav`, `.mds-workspace`, `.mds-toolbar`, and related primitives where they fit. Preserve native and ARIA state conventions, including `aria-current` for navigation and `data-tone` for supported status treatments.

Use upstream tokens and type roles rather than creating an independent visual system. Magpie uses Montserrat for titles/actions/navigation, Monda for headings, Lato for content, and platform monospace for code. Bundled fonts cover Latin; Chinese text uses system fallbacks.

Magpie does not supply a dedicated Kanban board, chat component, drag-and-drop system, virtualized list, or bounded board/chat split. Those are yesman components. Implement pane sizing and independent scrolling explicitly; the existing shell's sticky layout is not a complete desktop workspace.

Do not nest task cards inside additional generic cards. Use application-owned board columns and structural containers around Magpie card content. Keep status names available as text, rather than communicating state through color alone.

## Desktop window integration

Use native Tauri window decorations initially.

Magpie's current `initWindowChrome` helper targets raw WKWebView/WebView2 message bridges and is not a Tauri window integration. Do not enable it unchanged. Any later custom titlebar must use an explicit Tauri adapter and the corresponding narrowly scoped window permissions.

## Frontend-to-host boundary

The bundled frontend calls typed Tauri commands. The Rust bridge owns host addresses, stored connection credentials, HTTP/event connections, and reconnection. The frontend receives view data and event updates; it does not receive generic shell execution or unrestricted Herdr access.

Keep task mutations as domain commands. Moving an item out of **to triage** must record an actual user decision on its owning host. A visual board update alone cannot complete that operation.

Tauri capabilities and custom-command access must be configured explicitly. Loading a remote host's data does not require loading remote HTML into a privileged WebView.

## Implementation acceptance checks

When the UI is implemented, verify:

1. Magpie CSS, SVGs, and fonts load from packaged assets without a CDN.
2. Native Tauri chrome works without invoking Magpie's raw host bridge.
3. Long boards and conversations scroll independently inside a bounded window.
4. Keyboard interaction, focus, dialogs, and light/dark themes work in target WebViews.
5. Chinese content remains readable with system font fallbacks.
6. Offline/stale host state is visible, and failed commands do not appear committed.
7. Switching hosts and leads keeps conversations and commands attached to the correct owner.

These are future runtime checks, not checks already performed by this documentation change.

## References

- [Magpie README at the inspected revision](https://github.com/idy/magpie-design-system/blob/46ba098239979506a4ccc14721aac044483c66bc/README.md)
- [Magpie layout source](https://github.com/idy/magpie-design-system/blob/46ba098239979506a4ccc14721aac044483c66bc/css/layout.css)
- [Magpie JavaScript helpers](https://github.com/idy/magpie-design-system/blob/46ba098239979506a4ccc14721aac044483c66bc/js/magpie.js)
- [Tauri process model](https://v2.tauri.app/concept/process-model/)
- [Tauri capabilities](https://v2.tauri.app/security/capabilities/)
