/** Backend timestamps are Unix milliseconds, so gap thresholds must be ms. */
export const LONG_GAP_MS = 2 * 60 * 60 * 1000;

export function isLongGap(
  previous: number | undefined,
  current: number,
): boolean {
  return previous !== undefined && current - previous > LONG_GAP_MS;
}
