export function formatLocalTimestamp(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const time = date.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' })
  return `${time}.${String(date.getMilliseconds()).padStart(3, '0')}`
}
