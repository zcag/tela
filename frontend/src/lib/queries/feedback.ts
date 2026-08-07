import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '../api'
import { adminUsageKeys } from './admin-usage'
import type { CreateFeedbackInput } from '../types'

// POST /api/feedback — submit feedback from the in-app widget. The same backend
// core powers the MCP submit_feedback tool, so human + agent reports land in one
// inbox. Returns the created row (we ignore the body; the widget only needs ok).
export function useCreateFeedback() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: CreateFeedbackInput) =>
      api<{ feedback: { id: number } }>('/api/feedback', {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      // If the submitter happens to be an instance admin with the inbox open,
      // surface their own entry without a manual refetch.
      void qc.invalidateQueries({ queryKey: adminUsageKeys.feedbackAll })
    },
  })
}
