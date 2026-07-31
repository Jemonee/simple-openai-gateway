# SUPOS Industrial Theme

This is the repository's default visual direction for operational pages. Read this file before changing a Vue page, shared shell, or frontend styling. The current homepage is an example of the direction, not a restriction on future business layouts; a feature may use another visual language only when the user explicitly asks for it.

## Purpose and Mood

Use a quiet industrial workspace: precise, information-dense, flat, and easy to scan during repeated operations. The interface should feel like a control surface, not a marketing landing page. Use whitespace to separate work areas, while keeping navigation and status information compact.

## Layout Rules

- Keep a full-width blue utility header at 64-72px. Put product identity and tenant/context on the left; place icon-only utilities on the right.
- Use a light gray left navigation rail, normally 272-304px wide. It contains search, grouped menu labels, and compact rows. The active row uses a pale blue fill and blue text.
- Keep the workspace white or near-white with a thin top utility row. Content aligns to a 24-32px grid and should not float inside a decorative wrapper.
- At 960px and below, the rail becomes an off-canvas drawer. At 640px and below, reduce workspace padding to 16px and keep action rows wrapping.

## Tokens

Use the shared variables in `frontend/src/assets/supos-industrial.css` rather than inventing one-off colors:

- Header: `--supos-blue-600` / `--supos-blue-700`; soft selection: `--supos-blue-050`.
- Page and panels: `--supos-page`, `--supos-panel`, `--supos-line`.
- Text: `--supos-text`, `--supos-muted`, `--supos-subtle`.
- States: `--supos-success`, `--supos-warning`, `--supos-danger`.
- Prefer 1px borders and no shadow. If elevation is needed, use `--supos-shadow-soft` once, never stacked shadows.

## Components

- Use Element Plus icons or the existing icon library. Icon buttons are 32-36px square and need a `title` or tooltip.
- Buttons are rectangular with 3-6px radii. Reserve solid blue for the primary action; use outline or text actions for secondary work.
- Inputs and selects are 32-36px high with a 3px radius and visible focus rings.
- Cards are only for genuinely framed tools or repeated records. Avoid cards inside cards, giant rounded containers, gradients, glass effects, and decorative blobs.
- Status uses text plus a small dot or icon, not color alone. Keep contrast at WCAG AA where practical.

## Type and Content

Use the system sans-serif stack. Body text is 13-14px, navigation is 13px, page titles are 20-24px, and numeric metrics may use a monospace face. Use sentence case, short labels, and concrete operational language. Never copy company names or product labels from a reference image; consume identity from `project.json`.

## Review Checklist

Before handoff, check desktop and narrow mobile layouts, keyboard focus, loading/empty/error states, active navigation, icon tooltips, and that no text overlaps or depends on a fixed viewport width.
