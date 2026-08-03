# 红粉主题

## Purpose

“红粉”是 OpenAI 兼容网关管理界面的固定浅色主题。它面向流量检查、渠道比较、路由调整、令牌签发和重试诊断等高频操作，强调紧凑、稳定和清晰的信息呈现。

Design parameters are `variance 4 / motion 2 / density 7`. Layouts should be structured and information-dense, with restrained transitions used only for hover, focus, navigation, and state changes.

## Palette

- Page: `#FFF7FA`
- Surface: `#FFFCFD`
- Muted surface: `#FFF0F6`
- Primary: `#C62F67`
- Primary hover: `#A82055`
- Primary selection: `#FCE7F0`
- Text: `#2B2730`
- Muted text: `#716873`
- Border: `#EADDE3`
- Success: `#2F7D61`
- Warning: `#9A6700`
- Danger: `#B4233C`
- Supporting chart colors: teal `#2F7D61`, amber `#B7791F`, purple-gray `#736780`

All text and controls must meet WCAG AA contrast. Color is never the only status signal; pair it with a label, icon, or dot.

## Structure

- Use a near-white 62px top bar with a red-pink brand mark.
- Use a 252px pale-pink sidebar. The active menu item uses `#FCE7F0` with `#A82055` text.
- Use a full-width workspace with a 46px breadcrumb row and a maximum content width of 1480px.
- At 960px and below, turn the sidebar into an off-canvas drawer. At 640px and below, use 14px content padding and wrap toolbar actions.
- Keep tables and toolbars compact. Use a single framed surface around a table or form section, not cards nested inside cards.

## Components

- Panel radius: 6px. Button, input, select, tag, and menu row radius: 4px.
- Use 1px borders and no decorative shadows. Login may use a bordered authentication panel without elevation.
- Use icon plus text for commands where the label prevents ambiguity. Icon-only buttons require a title or tooltip.
- Use drawers or dialogs for create/edit flows and inline expansion for request attempts.
- Use skeletons for loading, direct inline errors, composed empty states, and short save confirmations.
- Numeric metrics and IDs use tabular figures or the monospace stack.
- Center labels and values inside metric or status blocks; keep tables, timelines, forms, and narrative content left-aligned for readability.

## Prohibited Treatments

Do not use gradients, glass effects, glow, decorative color blocks, bokeh, oversized radii, dark mode, marketing hero layouts, or background illustrations. Letter spacing remains zero. Motion must not change layout dimensions.

## Review Checklist

- Verify login, logout, password change, CRUD dialogs, one-time token display, filtering, pagination, and failure states.
- Verify desktop and mobile layouts with no clipped labels, overlapping controls, or horizontal page overflow.
- Verify keyboard focus, active navigation, status text, table empty states, and dialog focus behavior.
- Confirm `main.css` imports `hongfen-theme.css` and all pages use the shared theme tokens instead of isolated page palettes.
