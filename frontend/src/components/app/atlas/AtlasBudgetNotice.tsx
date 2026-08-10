import { AlertTriangle, Gauge } from 'lucide-react'
import { useAtlasBudget } from '../../../lib/queries/atlas'
import type { AtlasBudget } from '../../../lib/queries/atlas'

// AtlasBudgetNotice — how much Atlas indexing this account's schedule will cost,
// against what its plan allows.
//
// WHY IT EXISTS. The plan meters GPU MINUTES, but the user configures sources
// and a cadence — two steps removed, with a per-repo run cost they have never
// been shown. "Daily" costs 210 minutes a month on a small repo and 1,500 on a
// large one, so nobody can tell from the settings screen whether their choice
// fits. Without this the first signal is a 402 after the allowance is gone.
//
// The rules that keep it from becoming noise:
//   - It renders NOTHING when the projection fits, when the plan is unlimited,
//     or while loading. A banner that is always on stops being read.
//   - It never says "too frequent" without a number and a concrete alternative
//     computed from this account's own measured run cost.
//   - It distinguishes "you will exceed" from "you already have", because the
//     remedy differs: one is a setting to change, the other is a wait or an
//     upgrade.
//   - When any project lacks run history it says the figure is an estimate
//     rather than quietly presenting a guess as measurement.

function fmtMinutes(m: number): string {
  if (m >= 600) return `${Math.round(m / 60)}h`
  if (m >= 60) return `${(m / 60).toFixed(1)}h`
  return `${Math.round(m)} min`
}

const CADENCE_LABEL: Record<string, string> = {
  hourly: 'hourly',
  daily: 'daily',
  weekly: 'weekly',
  monthly: 'monthly',
}

export function AtlasBudgetNotice({ projectId }: { projectId?: number }) {
  const { data } = useAtlasBudget()
  if (!data || data.cap_minutes == null) return null

  // Scope to one project where asked (the settings screen), but keep the
  // ACCOUNT total as the verdict — the cap is per account, so a project that
  // looks modest on its own can still be the one that tips the total over.
  const scoped = projectId != null ? data.projects.find((p) => p.id === projectId) : undefined
  if (projectId != null && scoped && !scoped.auto_update && !data.over) return null

  const cap = data.cap_minutes
  const overSpent = data.used_minutes >= cap
  if (!data.over && !overSpent) return <BudgetMeter data={data} cap={cap} />

  return (
    <div
      role="status"
      className="flex flex-col gap-[var(--space-2)] rounded-[var(--radius-md)] border border-[var(--callout-warning-border)] bg-[var(--callout-warning-bg)] p-[var(--space-3)]"
    >
      <div className="flex items-start gap-[var(--space-2)]">
        <AlertTriangle className="mt-[2px] size-[var(--space-4)] shrink-0 text-[var(--callout-warning-fg)]" />
        <div className="flex flex-col gap-[var(--space-1)]">
          <p className="text-[length:var(--text-sm)] font-medium text-[var(--text-primary)]">
            {overSpent
              ? 'This month’s Atlas budget is used up'
              : 'This schedule will use more Atlas time than your plan allows'}
          </p>
          <p className="text-[length:var(--text-sm)] text-[var(--text-muted)]">
            {overSpent ? (
              <>
                {fmtMinutes(data.used_minutes)} of {fmtMinutes(cap)} used. Scheduled refreshes
                resume on the 1st.{' '}
              </>
            ) : (
              <>
                Your current schedule works out at about{' '}
                <strong className="text-[var(--text-primary)]">
                  {fmtMinutes(data.projected_minutes)}
                </strong>{' '}
                of indexing a month, against {fmtMinutes(cap)} on your plan.{' '}
              </>
            )}
            {data.estimated && 'Some projects have no run history yet, so this is an estimate. '}
          </p>

          {data.suggestion && (
            <p className="text-[length:var(--text-sm)] text-[var(--text-muted)]">
              Switching{' '}
              {data.suggestion.applies_to.length === 1 ? 'it' : `${data.suggestion.applies_to.length} projects`}{' '}
              to <strong className="text-[var(--text-primary)]">
                {CADENCE_LABEL[data.suggestion.cadence] ?? data.suggestion.cadence}
              </strong>{' '}
              would bring that to about {fmtMinutes(data.suggestion.projected_minutes)}.
            </p>
          )}
          {!data.suggestion && !overSpent && (
            // Even the slowest cadence doesn't fit — telling them to slow down
            // would be wrong, so say what's actually true.
            <p className="text-[length:var(--text-sm)] text-[var(--text-muted)]">
              Even monthly refreshes would exceed this plan’s budget, so a slower schedule won’t
              help — this needs fewer sources or more capacity.
            </p>
          )}
        </div>
      </div>
    </div>
  )
}

// The quiet state: usage without alarm, so the budget is visible before it bites
// rather than discovered when it does.
function BudgetMeter({ data, cap }: { data: AtlasBudget; cap: number }) {
  const pct = Math.min(100, Math.round((data.used_minutes / cap) * 100))
  return (
    <div className="flex flex-col gap-[var(--space-1)]">
      <div className="flex items-center gap-[var(--space-2)] text-[length:var(--text-xs)] text-[var(--text-muted)]">
        <Gauge className="size-[var(--space-3)]" />
        <span>
          {fmtMinutes(data.used_minutes)} of {fmtMinutes(cap)} Atlas time used this month
          {data.projected_minutes > 0 && <> · this schedule ≈ {fmtMinutes(data.projected_minutes)}/mo</>}
        </span>
      </div>
      <div className="h-[var(--space-1)] w-full overflow-hidden rounded-[var(--radius-sm)] bg-[var(--surface-2)]">
        <div className="h-full bg-[var(--accent)]" style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}
