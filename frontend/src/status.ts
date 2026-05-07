// Maps an HTTP status code to a CSS class for color coding.
// 5xx → error red, 4xx → client-error orange, 3xx → redirect, 2xx (and unknown) → success.
export function statusClass(status: number): string {
  if (status >= 500) return 'status-5'
  if (status >= 400) return 'status-4'
  if (status >= 300) return 'status-3'
  return 'status-200'
}

export function isErrorStatus(status: number): boolean {
  return status >= 400
}
