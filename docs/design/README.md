# Interface direction

Visual thesis: calm white and cool-gray surfaces, dark readable type, and one teal action color, with no decorative charts or invented telemetry.

Content plan: initial administrator setup, returning-user login, authenticated workspace overview, and account settings. Keep setup as an open form beside a progression rail. Use navigation and divided rows for the workspace, not a card mosaic. Add monitoring surfaces only when their backend behavior exists.

Interaction thesis: a short form entrance, clear hover/focus feedback, and explicit pending/error/success states. Respect reduced motion.

## Setup reference

`setup-concept.png` was generated with the built-in ImageGen tool and selected as the implementation reference (1536 × 1024). It is a design artifact, not shipped as UI. All text, controls, and icons are code-native. Lucide outline icons match the reference's search, arrow, and lock metaphors. No external font or image service is used at runtime.

Tokens: true white main background, cool gray `#f3f6f8` rail, slate `#101c26` text, muted `#66788c`, border `#dce3e8`, teal `#087f72`, 7px control radius. System sans-serif fallback deliberately keeps installation offline-capable. Primary form width is 534px at the reference viewport. Desktop rail is 30.6%; mobile replaces it with a compact brand header.

Allowed setup copy: sherlock; Your stack. Your evidence.; Reliability investigations, hosted by you.; Secure workspace; Connect a source; Map a service; Start monitoring; Read-only by design; SELF-HOSTED; Create your workspace; Set up the administrator account for this installation.; Setup token; Installation setup token; Find this in your local .env file.; Workspace name; My workspace; Email; you@example.com; Password; At least 12 characters; Create workspace; Credentials stay on your server.

Login reuses the same form system with Welcome back / Sign in. The first workspace used temporary release-status content; the delivered workspace now shows real monitoring, findings, investigations, and integration controls.


## Workspace redesign

The workspace uses grouped navigation (Workspace / Configuration), a 260px desktop rail, 44px content gutters, white surfaces, cool-gray `#f6f8fa` navigation, slate `#18252d` text and teal `#087f72` actions. Shared headings, status badges, controls and dividers replace page-specific styling. Report body text is 15px; primary page headings are 30px. There are no external runtime fonts or new UI dependencies.

[Settings concept](settings-v2-concept.png) and [investigation concept](investigation-v2-concept.png) are the 1536×1024 design references. These images are design artifacts, not shipped raster interfaces. Actual API state and original evidence wording remain authoritative. Provider names use text, navigation uses the existing Lucide metaphors, and the workspace name is not a fake switcher. Selected navigation uses the same restrained teal treatment throughout.

Settings exposes one section at a time: Account, Monitoring, AI providers, Notifications or System. Section hashes survive reloads and browser history. Provider rows precede consent/budgets and history, with usage and setup guidance in a secondary column. Connection, mapping, provider and destination forms use native modal drawers with focus cycling, Escape, and focus restoration.

Investigation views separate Report, AI analysis and Run activity. Reports put observations beside unknowns and suggested checks; supporting evidence expands under the observation that cites it. Findings keep investigation/evidence actions prominent and group lifecycle actions under Manage finding. Service environment and origin sit under the service name to reduce table width.

At tablet widths the secondary columns stack below the main content. At 700px and below, the sidebar becomes an expandable menu, forms use one column, and drawers fill the viewport. Settings navigation scrolls horizontally when necessary; tables retain their tabular structure in contained scroll regions. Motion is limited to short control/drawer transitions and respects reduced-motion preferences.
