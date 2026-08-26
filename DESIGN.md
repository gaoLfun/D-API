---
name: D-API Quiet Operations
description: A calm, light-first operational console with cold neutral layers and precise D-API green feedback.
colors:
  page: "#f4f6f4"
  surface: "#fbfcfb"
  surface-raised: "#f8faf8"
  surface-sunken: "#eef1ee"
  surface-hover: "#e8ede9"
  floating: "#fbfcfb"
  text: "#19211d"
  text-muted: "#5d6962"
  text-faint: "#68736d"
  line: "#dfe5e1"
  line-strong: "#c8d1cb"
  accent: "#176d4f"
  accent-hover: "#105b40"
  accent-soft: "#dcece5"
  accent-contrast: "#f8fbf9"
  info: "#2d6697"
  info-soft: "#e2edf6"
  warning: "#81530d"
  warning-soft: "#f5ead3"
  danger: "#a33c38"
  danger-hover: "#8b302d"
  danger-soft: "#f5e2e0"
  focus: "rgba(35, 115, 84, .28)"
  chart-bar: "#2f8968"
  chart-grid: "rgba(74, 91, 82, .12)"
  dark-page: "#151816"
  dark-surface: "#1b1f1c"
  dark-surface-raised: "#202521"
  dark-surface-sunken: "#252b27"
  dark-surface-hover: "#2b322d"
  dark-floating: "#252b27"
  dark-text: "#eef3ef"
  dark-text-muted: "#b6c0ba"
  dark-text-faint: "#8d9992"
  dark-line: "#313934"
  dark-line-strong: "#465149"
  dark-accent: "#4bb187"
  dark-accent-hover: "#61c49a"
  dark-accent-soft: "#173b2d"
  dark-accent-contrast: "#09140f"
  dark-info: "#73a7d2"
  dark-info-soft: "#1c3346"
  dark-warning: "#e1b45d"
  dark-warning-soft: "#3d3018"
  dark-danger: "#e07a74"
  dark-danger-hover: "#ed918b"
  dark-danger-soft: "#462623"
  dark-focus: "rgba(91, 194, 151, .32)"
  dark-chart-bar: "#48ad84"
  dark-chart-grid: "rgba(190, 205, 197, .11)"
typography:
  brand-display:
    fontFamily: "D-API CJK, Noto Sans CJK SC, Source Han Sans SC, Microsoft YaHei, PingFang SC, -apple-system, BlinkMacSystemFont, Segoe UI, sans-serif"
    fontSize: "112px"
    fontWeight: 800
    lineHeight: 0.78
    letterSpacing: "0"
  headline:
    fontFamily: "D-API CJK, Noto Sans CJK SC, Source Han Sans SC, Microsoft YaHei, PingFang SC, -apple-system, BlinkMacSystemFont, Segoe UI, sans-serif"
    fontSize: "27px"
    fontWeight: 700
    lineHeight: 1.18
    letterSpacing: "0"
  page-title:
    fontFamily: "D-API CJK, Noto Sans CJK SC, Source Han Sans SC, Microsoft YaHei, PingFang SC, -apple-system, BlinkMacSystemFont, Segoe UI, sans-serif"
    fontSize: "20px"
    fontWeight: 700
    lineHeight: 1.18
    letterSpacing: "0"
  title:
    fontFamily: "D-API CJK, Noto Sans CJK SC, Source Han Sans SC, Microsoft YaHei, PingFang SC, -apple-system, BlinkMacSystemFont, Segoe UI, sans-serif"
    fontSize: "16px"
    fontWeight: 700
    lineHeight: 1.3
    letterSpacing: "0"
  body:
    fontFamily: "D-API CJK, Noto Sans CJK SC, Source Han Sans SC, Microsoft YaHei, PingFang SC, -apple-system, BlinkMacSystemFont, Segoe UI, sans-serif"
    fontSize: "14px"
    fontWeight: 400
    lineHeight: "normal"
    letterSpacing: "0"
  control:
    fontFamily: "D-API CJK, Noto Sans CJK SC, Source Han Sans SC, Microsoft YaHei, PingFang SC, -apple-system, BlinkMacSystemFont, Segoe UI, sans-serif"
    fontSize: "13px"
    fontWeight: 680
    lineHeight: "normal"
    letterSpacing: "0"
  label:
    fontFamily: "D-API CJK, Noto Sans CJK SC, Source Han Sans SC, Microsoft YaHei, PingFang SC, -apple-system, BlinkMacSystemFont, Segoe UI, sans-serif"
    fontSize: "12px"
    fontWeight: 650
    lineHeight: "normal"
    letterSpacing: "0"
rounded:
  code: "4px"
  compact: "5px"
  choice: "6px"
  control: "7px"
  surface: "8px"
  switch: "10px"
  circle: "50%"
spacing:
  micro: "4px"
  xs: "7px"
  sm: "8px"
  compact: "10px"
  control: "14px"
  section: "18px"
  content: "28px"
components:
  button-primary:
    backgroundColor: "{colors.accent}"
    textColor: "{colors.accent-contrast}"
    typography: "{typography.control}"
    rounded: "{rounded.control}"
    padding: "0 14px"
    height: "38px"
  button-primary-hover:
    backgroundColor: "{colors.accent-hover}"
    textColor: "{colors.accent-contrast}"
    rounded: "{rounded.control}"
  button-secondary:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.text}"
    typography: "{typography.control}"
    rounded: "{rounded.control}"
    padding: "0 14px"
    height: "38px"
  icon-button:
    backgroundColor: "transparent"
    textColor: "{colors.text-muted}"
    rounded: "{rounded.control}"
    width: "36px"
    height: "36px"
  input:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.text}"
    typography: "{typography.body}"
    rounded: "{rounded.control}"
    padding: "9px 11px"
    height: "40px"
  panel:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.text}"
    rounded: "{rounded.surface}"
    padding: "18px"
  metric-card:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.text}"
    rounded: "{rounded.surface}"
    padding: "18px"
    height: "104px"
  status-good:
    backgroundColor: "{colors.accent-soft}"
    textColor: "{colors.accent}"
    rounded: "{rounded.compact}"
    padding: "0 7px"
    height: "24px"
  nav-active:
    backgroundColor: "{colors.accent-soft}"
    textColor: "{colors.accent}"
    typography: "{typography.label}"
    rounded: "{rounded.control}"
    padding: "0 11px"
    height: "40px"
---

# Design System: D-API Quiet Operations

## Overview

**Creative North Star: "Quiet Operations"**

Quiet Operations treats the console as a working instrument: cold neutral layers keep dense operational data calm, while D-API green marks routing health, selection, focus, and primary action with restraint. The visual system is light-first, but dark mode preserves the same hierarchy and semantic relationships rather than creating a separate personality.

Geometry is compact and deliberate. Surfaces settle on 8px corners, controls sit just inside them at 7px, and borders do most of the structural work. Feedback remains tactile through small lifts, clear focus rings, direct loading indicators, and short state transitions; motion never becomes decoration.

**Key Characteristics:**

- Cold neutral, light-first surfaces with a hierarchy-matched dark theme.
- D-API green reserved for action, selection, focus, and healthy state.
- 8px surface geometry with compact 7px controls and crisp 1px borders.
- Balanced operational density built for scanning tables, metrics, and forms.
- Restrained, immediate feedback with reduced-motion support.

## Colors

The palette combines near-neutral green-gray surfaces with one functional green accent, plus blue, amber, and red reserved for operational meaning.

### Primary

- **D-API Routing Green:** The main accent for primary actions, active navigation, focus, successful state, selection, and chart emphasis. Its dark-theme counterpart is lighter but keeps the same role.
- **Routing Green Soft:** The low-contrast fill behind active and healthy states; never use it as an ornamental page wash.

### Secondary

- **Signal Blue:** Informational metrics and notification-channel identity only.
- **Attention Amber:** Warning, paused, or degraded state only.
- **Failure Red:** Errors, destructive actions, failed states, and destructive confirmation only.

### Neutral

- **Cold Canvas:** The page-level background, visually behind every operational surface.
- **Working Surface:** Panels, fields, dialogs, and menus; raised and sunken variants separate table headers, codes, and secondary zones without decorative contrast.
- **Operational Ink:** Primary readable content, with muted ink for supporting metadata.
- **Quiet Line:** The default structural divider; strong line is reserved for fields, menus, and boundaries that need more definition.

### Named Rules

**The Green Has a Job Rule.** Green indicates action, selection, focus, health, or routing emphasis; it is not general decoration.

**The Semantic Quartet Rule.** Green, blue, amber, and red retain stable operational meanings in both themes, and state is always reinforced by text or iconography.

**The Theme Parity Rule.** Dark mode remaps tone and contrast while preserving hierarchy, component geometry, and semantic roles.

## Typography

**Display Font:** D-API CJK, backed by the local CJK sans stack.
**Body Font:** D-API CJK, backed by the local CJK sans stack.
**Label/Mono Font:** D-API CJK for labels; the platform monospace stack for identifiers, priorities, and protocol choices.

**Character:** A single utilitarian CJK sans family keeps Chinese operational copy compact and consistent. Weight and size establish hierarchy without letter-spacing effects; tabular numerals and monospace are used only where exact comparison matters.

### Hierarchy

- **Brand Display** (800, 112px, 0.78): Login wordmark only; do not reuse it for page headings.
- **Headline** (700, 27px, 1.18): Login and rare high-level entry headings.
- **Page Title** (700, 20px, 1.18): Sticky workspace header titles.
- **Title** (700, 16px, 1.3): Panel, modal, drawer, and section titles.
- **Body** (400, 14px, normal): Default interface copy and form values.
- **Control** (680, 13px, normal): Primary, secondary, text, and danger button labels.
- **Label** (650, 12px, normal): Field labels and compact operational metadata.

### Named Rules

**The Zero Tracking Rule.** All interface typography uses zero letter spacing; hierarchy comes from size, weight, and placement.

**The Numbers Align Rule.** Metrics and balances use tabular numerals; identifiers and routing primitives may use monospace, but prose does not.

## Layout

The desktop shell uses a fixed 232px sidebar that can collapse to 68px, a 72px sticky top bar, and a centered content region capped at 1560px. Horizontal content padding scales between 20px and 46px, while major view sections follow an 18px vertical rhythm. Metric summaries use four equal columns, dropping to two below 1100px; notification cards use two columns before becoming a single column at the same breakpoint.

At 760px, the sidebar becomes a 278px maximum off-canvas sheet, the top bar reduces to 64px, and content padding tightens to 13px. Dense tables preserve comparison by scrolling horizontally; the two primary entity tables hide secondary columns and expose those details in explicit expandable rows. Forms collapse to one column, dialogs become bottom sheets, and drawers become full width. At 440px, metric and action layouts become single-column.

**The Density Without Compression Rule.** Keep the observed 42px header and 58px table-row rhythm on desktop; reduce columns responsively before reducing legibility or action target size.

**The Context Stays Put Rule.** Headers, table headers, dialog headers, and action footers remain sticky where the implementation already makes long operational flows scroll.

## Elevation & Depth

The system is flat by default. Page, sunken, working, and raised tones plus 1px borders establish most depth. Strong ambient shadows appear only on transient floating menus, modals, drawers, toasts, the mobile navigation sheet, and the sidebar collapse handle. The sticky top bar uses a restrained translucent layer and blur, with a solid page fallback when reduced transparency is requested.

### Shadow Vocabulary

- **Floating Ambient:** A broad, soft shadow for menus, modals, and toasts.
- **Drawer Directional:** A left-cast shadow that separates a right-side configuration drawer from the workspace.
- **Control Lift:** A small hover lift for primary and secondary buttons; it disappears on active press.

### Named Rules

**The Flat Until Floating Rule.** Resting panels and metric cards have no outer shadow; elevation is reserved for transient layers and direct interaction feedback.

## Shapes

The recurring silhouette is compact geometry with gently curved edges. Panels, cards, menus, and desktop dialogs use 8px corners; buttons and fields use 7px; compact tags and states use 4-5px. Circular geometry is reserved for avatars, status dots, switch thumbs, and numbered attempt markers. Mobile dialogs keep only their top 8px corners when attached to the viewport edge.

**The Nested Radius Rule.** Child controls use a radius one step tighter than the 8px surface that contains them.

**The Border Before Shadow Rule.** Use the quiet 1px line to define resting objects; do not add a card shadow to compensate for weak structure.

## Components

### Buttons

- **Shape:** Compact rectangular controls with gently curved 7px corners and a 38px minimum height.
- **Primary:** Routing green fill, high-contrast text, 14px horizontal padding, and a quiet one-pixel resting shadow.
- **Hover / Focus:** Hover darkens or lightens the semantic fill by theme and lifts 1px; focus uses a 3px green-tinted ring with 2px offset; active returns to baseline and scales to 98%.
- **Secondary / Text / Danger:** Secondary uses a strong line on the working surface; text buttons remove the container; danger uses the red semantic pair. Icon-only actions use stable 36px squares and familiar Lucide symbols with titles.

### Chips

- **Style:** Protocol tags use a raised neutral fill, quiet border, compact 4px corners, and restrained text. Status badges use semantic soft fills, colored text, and a 6px dot plus explicit text.
- **State:** Choice chips in forms gain a green border and soft green fill when selected. They remain controls, not decorative labels.

### Cards / Containers

- **Corner Style:** 8px for panels, metrics, filters, notification cards, menus, and dialogs.
- **Background:** Working surfaces over the cold canvas; raised and sunken tones identify nested regions.
- **Shadow Strategy:** Flat at rest; see Elevation & Depth for floating layers.
- **Border:** One-pixel quiet line by default.
- **Internal Padding:** Typically 14-18px, with 28px reserved for large empty states.

### Inputs / Fields

- **Style:** 40px minimum height, 7px corners, strong neutral stroke, working-surface fill, and 9px by 11px padding.
- **Focus:** Accent border plus a 3px semantic focus ring.
- **Error / Disabled:** Error copy and destructive states use failure red; disabled controls retain shape and reduce opacity without losing their label.

### Navigation

Desktop navigation sits in the sunken sidebar with 40px rows and 7px corners. Default items use muted ink; hover uses a subtle neutral fill and 2px horizontal nudge; active uses routing-green text and soft fill. The collapsed rail preserves icon actions and titles. On mobile the full navigation slides in as an off-canvas sheet over a darkened backdrop.

### Operational Tables

Tables use sticky raised headers, 58px rows, quiet dividers, tabular numbers, compact sorting controls, visible status text, and contextual row actions. Hover uses a low-opacity accent wash. Request attempts expand in place; mobile entity details use explicit disclosure rows rather than shrinking every column.

### Drawers, Dialogs, and Feedback

Long upstream configuration uses a right-side 720px maximum drawer; shorter forms and confirmations use centered dialogs up to 520px. Mobile dialogs attach to the bottom edge and drawers fill the viewport. Toasts, loading lines, pressed states, and brief entrance motion make actions legible without interrupting the operator.

### Usage Chart

The usage view switches between request, Token, cache, and latency modes. It
combines compact bars with line series only when comparison benefits from both:
requests versus successes, input/output/cache Token, cache reads versus writes,
and average versus P95 latency. The x-axis has no grid, y-axis lines stay quiet,
tooltips are restrained, bars use 3px corners and a 22px maximum thickness, and
long dimension labels are truncated without resizing the canvas. Theme colors
come from the same semantic CSS variables as the rest of the interface, and
animation is disabled when reduced motion is requested.

## Do's and Don'ts

### Do:

- **Do** preserve the shared semantic hierarchy when adding a dark-theme value.
- **Do** keep primary actions visible and move uncommon or destructive row actions into contextual menus.
- **Do** use borders and tonal layers for resting structure, reserving shadows for transient elevation.
- **Do** maintain text or icon reinforcement for every colored operational state.
- **Do** keep mobile workflows complete through off-canvas navigation, stacked forms, and explicit detail disclosure.
- **Do** honor reduced motion and reduced transparency preferences.

### Don't:

- **Don't** use D-API green as a decorative wash or a second background palette.
- **Don't** add oversized cards, marketing-style hero composition, or decorative rounded containers to operational views.
- **Don't** shrink dense tables until labels or actions become illegible; reduce columns or allow controlled scrolling.
- **Don't** place panels inside decorative cards or add shadows to resting panels.
- **Don't** introduce a separate dark-mode identity, geometry, or semantic color meaning.
- **Don't** animate for atmosphere; motion must explain state, entry, loading, or direct manipulation.
