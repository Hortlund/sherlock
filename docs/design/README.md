# Interface direction

Visual thesis: calm white and cool-gray surfaces, dark readable type, and one teal action color, with no decorative charts or invented telemetry.

Content plan: initial administrator setup, returning-user login, authenticated workspace overview, and account settings. Keep setup as an open form beside a progression rail. Use navigation and divided rows for the workspace, not a card mosaic. Add monitoring surfaces only when their backend behavior exists.

Interaction thesis: a short form entrance, clear hover/focus feedback, and explicit pending/error/success states. Respect reduced motion.

## Setup reference

`setup-concept.png` was generated with the built-in ImageGen tool and selected as the implementation reference (1536 × 1024). It is a design artifact, not shipped as UI. All text, controls, and icons are code-native. Lucide outline icons match the reference's search, arrow, and lock metaphors. No external font or image service is used at runtime.

Tokens: true white main background, cool gray `#f3f6f8` rail, slate `#101c26` text, muted `#66788c`, border `#dce3e8`, teal `#087f72`, 7px control radius. System sans-serif fallback deliberately keeps installation offline-capable. Primary form width is 534px at the reference viewport. Desktop rail is 30.6%; mobile replaces it with a compact brand header.

Allowed setup copy: sherlock; Your stack. Your evidence.; Reliability investigations, hosted by you.; Secure workspace; Connect a source; Map a service; Start monitoring; Read-only by design; SELF-HOSTED; Create your workspace; Set up the administrator account for this installation.; Setup token; Installation setup token; Find this in your local .env file.; Workspace name; My workspace; Email; you@example.com; Password; At least 12 characters; Create workspace; Credentials stay on your server.

Login reuses the same form system with Welcome back / Sign in. The initial workspace explicitly states that source monitoring is not yet implemented; subsequent slices replace that temporary release-status content with actual functionality.
