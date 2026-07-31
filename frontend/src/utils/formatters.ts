const durationNumberFormatter = new Intl.NumberFormat('zh-CN', {
  maximumFractionDigits: 2,
})

const compactNumberFormatter = new Intl.NumberFormat('en-US', {
  maximumFractionDigits: 2,
})

/** Formats a millisecond duration using the largest supported practical unit. */
export function formatDuration(milliseconds: number): string {
  if (!Number.isFinite(milliseconds)) return '--'

  const value = Math.max(0, milliseconds)
  if (value < 1_000) return `${Math.round(value)}ms`
  if (value < 60_000) return `${durationNumberFormatter.format(value / 1_000)}s`
  if (value < 3_600_000) return `${durationNumberFormatter.format(value / 60_000)}m`
  if (value < 86_400_000) return `${durationNumberFormatter.format(value / 3_600_000)}h`
  return `${durationNumberFormatter.format(value / 86_400_000)}d`
}

export function formatCompactNumber(value: number): string {
  if (!Number.isFinite(value)) return '--'

  const absoluteValue = Math.abs(value)
  if (absoluteValue >= 1_000_000_000) return `${compactNumberFormatter.format(value / 1_000_000_000)}B`
  if (absoluteValue >= 1_000_000) return `${compactNumberFormatter.format(value / 1_000_000)}M`
  if (absoluteValue >= 1_000) return `${compactNumberFormatter.format(value / 1_000)}K`
  return compactNumberFormatter.format(value)
}
