import { AlertTriangle } from 'lucide-react'
import { useMyUsage } from '../../../lib/queries/billing'

// Atlas quota meters for the caller's personal account.
//
// This exists because the limits are invisible until they bite: the cost of a
// run is decided by repo size and by how dense a source's extracted surface is,
// neither of which a user can see. Being refused at the moment you press
// "Generate", with no prior signal, reads as breakage rather than a plan
// boundary — so show the meters continuously and warn before the wall, not at it.
//
// Personal account only: org-owned projects meter against the org, which has its
// own usage view, and mixing the two in one strip would misreport both.

function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1).replace(/\.0$/, '')}M`
  if (n >= 1_000) return `${Math.round(n / 1000)}k`
  return String(n)
}

type Meter = { label: string; used: number; limit: number; render: (n: number) => string }

function Bar({ m }: { m: Meter }) {
  const pct = Math.min(100, Math.round((m.used / m.limit) * 100))
  const over = m.used >= m.limit
  const near = !over && pct >= 80
  const tone = over
    ? 'var(--danger, #dc2626)'
    : near
      ? 'var(--warning, #d97706)'
      : 'var(--accent)'
  return (
    <div className="min-w-[12rem] flex-1">
      <div className="flex items-baseline justify-between gap-[var(--space-2)]">
        <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">{m.label}</span>
        <span
          className="text-[length:var(--text-xs)] tabular-nums"
          style={{ color: over || near ? tone : 'var(--text-muted)' }}
        >
          {m.render(m.used)} / {m.render(m.limit)}
        </span>
      </div>
      <div className="mt-[var(--space-1)] h-[var(--space-1)] overflow-hidden rounded-[var(--radius-sm)] bg-[var(--surface-3,var(--surface-2))]">
        <div className="h-full rounded-[var(--radius-sm)]" style={{ width: `${pct}%`, background: tone }} />
      </div>
    </div>
  )
}

export function AtlasQuotaBar() {
  const { data } = useMyUsage()
  if (!data) return null

  const { plan, usage } = data
  const meters: Meter[] = []
  if (plan.max_atlas_runs_per_month != null) {
    meters.push({
      label: 'Runs this month',
      used: usage.atlas_runs,
      limit: plan.max_atlas_runs_per_month,
      render: String,
    })
  }
  if (plan.max_embed_tokens_per_month != null) {
    meters.push({
      label: 'Embedding tokens this month',
      used: usage.embed_tokens,
      limit: plan.max_embed_tokens_per_month,
      render: fmtTokens,
    })
  }
  // An unlimited tier has null caps and gets no strip at all.
  if (meters.length === 0) return null

  const blocked = meters.some((m) => m.used >= m.limit)

  return (
    <div
      className="mt-[var(--space-4)] rounded-[var(--radius-md)] border p-[var(--space-3)]"
      style={{
        borderColor: blocked ? 'var(--danger, #dc2626)' : 'var(--border)',
        background: blocked ? 'color-mix(in srgb, var(--danger, #dc2626) 6%, transparent)' : 'var(--surface-2)',
      }}
    >
      {blocked && (
        <p className="mb-[var(--space-3)] flex items-start gap-[var(--space-2)] text-[length:var(--text-sm)] text-[var(--text-primary)]">
          <AlertTriangle className="mt-[0.15em] size-[var(--space-4)] shrink-0" style={{ color: 'var(--danger, #dc2626)' }} />
          <span>
            <strong>Over the {plan.name} plan limit.</strong> New Atlas runs are paused until the
            budget resets on the 1st. Existing docs stay published and searchable — nothing is
            removed.
          </span>
        </p>
      )}
      <div className="flex flex-wrap gap-[var(--space-4)]">
        {meters.map((m) => (
          <Bar key={m.label} m={m} />
        ))}
      </div>
      {plan.max_atlas_files_per_run != null && (
        <p className="mt-[var(--space-3)] text-[length:var(--text-xs)] text-[var(--text-muted)]">
          The {plan.name} plan indexes up to{' '}
          <span className="tabular-nums">{plan.max_atlas_files_per_run.toLocaleString()}</span> files
          per run. A larger source is refused before anything is generated — narrow it with the
          source&rsquo;s subpath or include/exclude filters, or upgrade.
        </p>
      )}
    </div>
  )
}
