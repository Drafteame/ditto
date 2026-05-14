import type { Sequence } from './types'

// Given the step just served (1-based), compute the cursor the backend will
// read on the next call. Mirrors the advance logic in mock.go:MatchAndResolve.
export function nextCursor(served: number, len: number, onEnd: Sequence['on_end']): number {
  if (len <= 0) return 0
  if (onEnd === 'stay') return Math.min(served, len - 1)
  if (onEnd === 'reset' || onEnd === 'proxy') return served
  return served % len // loop
}

export interface SequenceDisplay {
  label: string
  tooltip: string
  fallbackNext: boolean
  proxyNext: boolean
}

// Describe how to render a sequence's "next call" hint.
export function describeSequence(sequence: Sequence | undefined): SequenceDisplay | null {
  const steps = sequence?.steps ?? []
  if (!sequence || steps.length === 0) return null
  const total = steps.length
  const cursor = sequence.current_step ?? 0
  const fallbackNext = sequence.on_end === 'reset' && cursor >= total
  const proxyNext = sequence.on_end === 'proxy' && cursor >= total
  const nextStep = (cursor % total) + 1
  return {
    label: fallbackNext ? `↺/${total}` : proxyNext ? `→ real` : `${nextStep}/${total}`,
    tooltip: fallbackNext
      ? `Sequence — ${total} steps, next returns the fallback body`
      : proxyNext
        ? `Sequence — ${total} steps exhausted, next request goes to the real backend`
        : `Sequence — ${total} steps, next returns step ${nextStep}`,
    fallbackNext,
    proxyNext,
  }
}
