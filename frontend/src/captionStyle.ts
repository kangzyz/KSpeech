import type { CSSProperties } from 'vue'
import { prefersDark } from './theme'

const TEXT_ALIGNMENTS = ['left', 'center', 'right', 'justify'] as const

/** Converts a legacy 0xAARRGGBB configuration integer to a CSS colour. */
export const argbToCss = (value: unknown): string => {
  const number = Number(value) >>> 0
  const alpha = ((number >>> 24) & 0xff) / 255
  const red = (number >>> 16) & 0xff
  const green = (number >>> 8) & 0xff
  const blue = number & 0xff
  return `rgba(${red}, ${green}, ${blue}, ${alpha.toFixed(3)})`
}

/** Opaque form of an ARGB value, for swatches that render alpha separately. */
export const argbToHex = (value: unknown): string =>
  `#${((Number(value) >>> 0) & 0xffffff).toString(16).padStart(6, '0')}`

/**
 * The appearance page still measures the font the way the old full-width
 * caption did: one line across the whole window, so 48px was a sensible
 * default. The message stream is a column of them, so the configured size is
 * mapped into a range a scrolling list can actually show. The mapping keeps the
 * relative difference — a larger setting is still visibly larger — and the same
 * function feeds the settings preview so the two never drift apart.
 */
export const messageFontSize = (config: Record<string, unknown>): number => {
  const configured = Number(config['appearance.FontSize'] ?? 48)
  if (!Number.isFinite(configured)) return 20
  return Math.min(34, Math.max(12, Math.round(configured * 0.42)))
}

/** WCAG relative luminance of an sRGB channel triple. */
const luminance = (red: number, green: number, blue: number): number => {
  const channel = (value: number) => {
    const ratio = value / 255
    return ratio <= 0.03928 ? ratio / 12.92 : ((ratio + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel(red) + 0.7152 * channel(green) + 0.0722 * channel(blue)
}

/*
 * Luminance of the frosted panel the captions land on, per theme: --overlay-bg
 * composited over a mid-grey desktop. The panel is translucent, so this is an
 * estimate — but the 82%/88% alpha dominates, and the decision below only needs
 * to tell "readable" from "invisible".
 */
const PANEL_LUMINANCE = { dark: 0.03, light: 0.84 }
/** WCAG AA for large text. Caption text is always at least 12px and bold-ish. */
const MINIMUM_CONTRAST = 3

/**
 * The caption colour to draw with, or null to inherit the theme's own.
 *
 * The appearance page predates the console: its colours were chosen for a
 * caption floating on unknown desktop content, where white text with a black
 * outline is the only safe combination. Those same values are invisible on the
 * console's light frosted panel, and every installation carries them, so a
 * colour that cannot be read against the panel steps aside for the theme's.
 * A colour that *can* be read is used exactly as configured.
 */
const captionInk = (config: Record<string, unknown>): { colour: string; shadow: string } | null => {
  const value = Number(config['appearance.FontColor'] ?? 0xffffffff) >>> 0
  const alpha = ((value >>> 24) & 0xff) / 255
  const panel = prefersDark.value ? PANEL_LUMINANCE.dark : PANEL_LUMINANCE.light
  // Compositing the colour over the panel folds transparency into the same test.
  const ink = alpha * luminance((value >>> 16) & 0xff, (value >>> 8) & 0xff, value & 0xff) + (1 - alpha) * panel
  const contrast = (Math.max(ink, panel) + 0.05) / (Math.min(ink, panel) + 0.05)
  if (contrast < MINIMUM_CONTRAST) return null

  // The shadow is part of the same overlay-era pairing, so it only applies to a
  // colour the user can actually see; on the panel it is a soft halo rather
  // than the heavy outline bare desktop needed.
  const shadowSize = Number(config['appearance.ShadowSize'] ?? 10)
  const shadowColour = argbToCss(config['appearance.ShadowColor'] ?? 0xff000000)
  return {
    colour: argbToCss(value),
    shadow:
      shadowSize > 0
        ? `0 1px ${Math.max(1, Math.round(shadowSize / 3))}px ${shadowColour}`
        : 'none',
  }
}

/** Text style for one line of recognized speech in the message stream. */
export const messageStyleFrom = (config: Record<string, unknown>): CSSProperties => {
  const alignment = TEXT_ALIGNMENTS[Number(config['appearance.TextAlign'] ?? 0)] || 'left'
  const ink = captionInk(config)
  return {
    fontFamily: String(config['appearance.FontFamily'] ?? 'Arial'),
    fontSize: `${messageFontSize(config)}px`,
    textAlign: alignment as CSSProperties['textAlign'],
    // Left unset, the caption inherits the console's own --overlay-text.
    ...(ink ? { color: ink.colour, textShadow: ink.shadow } : {}),
  }
}

/** Whether the configured caption colour is being used as-is. */
export const captionColourApplies = (config: Record<string, unknown>): boolean =>
  captionInk(config) !== null

export interface ConsoleStyle extends CSSProperties {
  '--console-tint'?: string
  '--console-hover'?: string
}

/**
 * Tint layers for the console shell. Both are painted over the frosted
 * backdrop, so the configured colours keep meaning what they always meant:
 * a resting tint and the highlight the window takes while hovered.
 */
export const consoleStyleFrom = (config: Record<string, unknown>): ConsoleStyle => ({
  '--console-tint': argbToCss(config['appearance.BackgroundColor'] ?? 0),
  '--console-hover': argbToCss(config['appearance.MouseHover'] ?? 0x2709a9ff),
})
