import type { LocationQueryRaw, LocationQueryValue } from 'vue-router'

type HeadlessQueryValue = LocationQueryValue | LocationQueryValue[] | undefined

/** Whether the URL query explicitly enables the content-only application shell. */
export function isHeadlessMode(value: HeadlessQueryValue): boolean {
  const values = Array.isArray(value) ? value : [value]
  return values.some((item) => (
    item === null
    || item === ''
    || item === '1'
    || item?.toLowerCase() === 'true'
  ))
}

/** Preserve headless mode when a page performs an in-app navigation. */
export function preserveHeadlessQuery(
  value: HeadlessQueryValue,
  query: LocationQueryRaw = {},
): LocationQueryRaw {
  return isHeadlessMode(value) ? { ...query, headless: '1' } : query
}
