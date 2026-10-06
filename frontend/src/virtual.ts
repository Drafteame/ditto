export function virtualRange(scrollTop: number, viewportHeight: number, rowHeight: number, count: number, overscan = 8) {
  const start = Math.min(count, Math.max(0, Math.floor(scrollTop / rowHeight) - overscan))
  const end = Math.max(start, Math.min(count, Math.ceil((scrollTop + viewportHeight) / rowHeight) + overscan))
  return { start, end, top: start * rowHeight, bottom: (count - end) * rowHeight }
}
