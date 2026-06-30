import { ArrowDownRight, ArrowUpRight, ExternalLink, Info } from 'lucide-react'
import { navigateToPage } from '../../lib/pageHitItem'
import { useAIUsage } from '../../lib/queries/ai-usage'
import { useAIEndpoints, type AIEndpointHealth, type AIGate } from '../../lib/queries/ai-endpoints'
import {
  useAdminStats,
  type StatsSignup,
  type StatsTopPage,
  type StatsTopPerson,
  type StatsTopSpace,
  type StatsUnanswered,
} from '../../lib/queries/admin-stats'
import { Avatar, type AvatarTone } from '../ui/avatar'
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from '../ui/card'
import { CoverageGauge } from '../ui/coverage-gauge'
import { Sparkline } from '../ui/sparkline'
import { StatusBadge } from '../ui/status-badge'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '../ui/tooltip'
import { relativeTimeFromSqlite } from '../../lib/relativeTime'
import { cn } from '../../lib/utils'

// Settings → Insights — the instance-analytics hub (instance-admin). A scannable
// top-to-bottom read: at-a-glance KPIs → growth/signups → activity → people &
// content → AI/retrieval → knowledge health. Everything is aggregated server-side
// from the events log + existing tables; week-over-week deltas are derived here
// from the dense 30-day series. See backend admin_stats.go.

const nf = (n: number) => n.toLocaleString()
const sum = (a: number[]) => a.reduce((x, y) => x + y, 0)
const last7 = (a: number[]) => sum(a.slice(-7))
const prev7 = (a: number[]) => sum(a.slice(-14, -7))
// New in the trailing 7 days, read off a cumulative series.
const newInWeek = (cum: number[]) =>
  cum.length >= 8 ? cum[cum.length - 1] - cum[cum.length - 8] : (cum[cum.length - 1] ?? 0) - (cum[0] ?? 0)

export function SettingsInsightsTab() {
  const q = useAdminStats()

  if (q.isLoading) return <InsightsSkeleton />
  if (q.isError || !q.data) {
    return (
      <p role="alert" className="m-0 text-[length:var(--text-sm)] text-[var(--danger)]">
        Couldn't load insights.
      </p>
    )
  }
  const s = q.data
  const answerRate = s.asks_30 > 0 ? s.asks_answered_30 / s.asks_30 : 0
  const activation = s.users > 0 ? s.activated / s.users : 0

  return (
    <TooltipProvider delayDuration={150}>
      <section aria-labelledby="settings-insights" className="flex flex-col gap-[var(--space-6)]">
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)] leading-[var(--leading-relaxed)]">
          A live read on the instance. Trends are daily over the last 30 days; deltas
          compare the last 7 days with the 7 before.
        </p>

        {/* At a glance */}
        <div className="grid grid-cols-2 lg:grid-cols-4 gap-[var(--space-3)]">
          <Kpi
            label="Active users"
            value={s.wau}
            delta={s.wau - s.wau_prev}
            deltaSuffix="vs last wk"
            sub={`${nf(s.dau)} today · ${nf(s.mau)} this month`}
            hint="Distinct users with any logged activity in the last 7 days. The delta compares against the 7 days before that."
          />
          <Kpi
            label="Users"
            value={s.users}
            delta={newInWeek(s.users_cum)}
            deltaSuffix="this week"
            series={s.users_cum}
            accent
          />
          <Kpi
            label="Pages"
            value={s.pages}
            delta={newInWeek(s.pages_cum)}
            deltaSuffix="this week"
            series={s.pages_cum}
            accent
          />
          <Kpi
            label="Agents connected"
            value={s.mcp_users}
            sub={`${nf(s.active_pats)} active token${s.active_pats === 1 ? '' : 's'} · ${nf(s.public_spaces)} public`}
            hint="Users who have connected an agent over MCP — tela's first-class audience. Active tokens are live personal access tokens; public = spaces published to the open web."
            accent
          />
        </div>

        {/* Growth & signups */}
        <Section title="Signups & activation" desc="Who's joining, and whether they do anything once they're in.">
          <div className="grid gap-[var(--space-5)] lg:grid-cols-[auto_1fr] items-start">
            <div className="flex items-center gap-[var(--space-4)] lg:flex-col lg:items-start">
              <CoverageGauge value={activation} caption="activated" size="md" />
              <div className="flex flex-col gap-[var(--space-1)]">
                <span className="text-[length:var(--text-sm)] text-[var(--text-primary)]">
                  <span className="font-semibold tabular-nums">{nf(s.activated)}</span>
                  <span className="text-[var(--text-muted)]"> of {nf(s.users)}</span>
                </span>
                <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">
                  have written a page
                </span>
                <span className="mt-[var(--space-2)] text-[length:var(--text-sm)] text-[var(--text-primary)]">
                  <span className="font-semibold tabular-nums">{nf(s.new_users_30)}</span>
                  <span className="text-[var(--text-muted)]"> new · 30d</span>
                </span>
              </div>
            </div>
            <Panel title="Recent signups" empty="No signups yet.">
              {s.recent_signups.map((u) => (
                <SignupRow key={u.user_id} u={u} />
              ))}
            </Panel>
          </div>
        </Section>

        {/* Activity */}
        <Section title="Activity" desc="Daily volume over 30 days, with the week-over-week change.">
          <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-5 gap-[var(--space-3)]">
            <Trend label="Page views" series={s.views} />
            <Trend label="Edits" series={s.edits} />
            <Trend label="Sign-ins" series={s.logins} />
            <Trend label="Asks" series={s.asks} />
            <Trend label="Errors" series={s.errors} invert tone="danger" />
          </div>
        </Section>

        {/* People & content */}
        <Section title="Top content & people" desc="The most active pages, contributors, and spaces over 30 days.">
          <div className="grid grid-cols-1 lg:grid-cols-3 gap-[var(--space-4)]">
            <Panel title="Most-viewed pages" empty="No views yet.">
              {s.top_pages.map((p, i) => (
                <PageRow key={p.page_id} p={p} rank={i + 1} />
              ))}
            </Panel>
            <Panel title="Top contributors" empty="No edits yet.">
              {s.top_contributors.map((c, i) => (
                <ContributorRow key={c.label} c={c} rank={i + 1} />
              ))}
            </Panel>
            <Panel title="Most-active spaces" empty="No activity yet.">
              {s.top_spaces.map((sp, i) => (
                <SpaceRow key={sp.space_id} s={sp} rank={i + 1} />
              ))}
            </Panel>
          </div>
        </Section>

        {/* AI & retrieval */}
        <Section title="AI & retrieval" desc="Ask usage and the questions your docs couldn't answer — a to-do list for what to write.">
          <div className="mb-[var(--space-3)] flex items-center gap-[var(--space-2)]">
            <span className={cn(
              'inline-flex items-center gap-[var(--space-1)] rounded-full px-[var(--space-2)] py-0.5',
              'text-[length:var(--text-xs)] font-medium',
              s.ai_healthy
                ? 'bg-[var(--accent-positive-subtle)] text-[var(--accent-positive-fg)]'
                : 'bg-[var(--accent-negative-subtle)] text-[var(--accent-negative-fg)]',
            )}>
              <span className={cn('h-1.5 w-1.5 rounded-full', s.ai_healthy ? 'bg-[var(--accent-positive-fg)]' : 'bg-[var(--accent-negative-fg)]')} />
              {s.ai_healthy ? 'AI available' : 'AI unavailable'}
            </span>
            {!s.ai_healthy && s.ai_reason && (
              <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">{s.ai_reason}</span>
            )}
          </div>
          <div className="grid gap-[var(--space-5)] lg:grid-cols-[auto_1fr] items-start">
            <div className="flex items-center gap-[var(--space-4)] lg:flex-col lg:items-start">
              <CoverageGauge value={answerRate} caption="answered" size="md" />
              <div className="flex flex-col gap-[var(--space-1)]">
                <span className="text-[length:var(--text-sm)] text-[var(--text-primary)]">
                  <span className="font-semibold tabular-nums">{nf(s.asks_30)}</span>
                  <span className="text-[var(--text-muted)]"> asks · 30d</span>
                </span>
                <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">
                  {nf(s.asks_answered_30)} returned grounding
                </span>
              </div>
            </div>
            <Panel
              title="Unanswered questions"
              empty="Every recent question found something."
            >
              {s.unanswered_asks.map((a, i) => (
                <UnansweredRow key={i} a={a} />
              ))}
            </Panel>
          </div>
        </Section>

        <AIReliabilitySection />

        <AIUsageSection />

        {/* Knowledge health */}
        <Section title="Knowledge health" desc="Signals worth a periodic tidy-up.">
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-[var(--space-3)]">
            <Health label="Stale pages" value={s.stale_pages} sub="90d+ untouched" warn={s.stale_pages > 0} />
            <Health label="Orphan pages" value={s.orphan_pages} sub="no inbound links" warn={s.orphan_pages > 0} />
            <Health label="Contradictions" value={s.contradictions} sub="pages that disagree" warn={s.contradictions > 0} />
          </div>
        </Section>
      </section>
    </TooltipProvider>
  )
}

// --- Composition primitives -------------------------------------------------

function Section({ title, desc, children }: { title: string; desc?: string; children: React.ReactNode }) {
  return (
    <Card>
      <CardHeader className="pb-[var(--space-4)]">
        <CardTitle className="text-[length:var(--text-base)]">{title}</CardTitle>
        {desc ? <CardDescription>{desc}</CardDescription> : null}
      </CardHeader>
      <CardBody className="pb-[var(--space-6)]">{children}</CardBody>
    </Card>
  )
}

function Kpi({
  label,
  value,
  sub,
  delta,
  deltaSuffix,
  deltaInvert,
  series,
  accent,
  hint,
}: {
  label: string
  value: number
  sub?: string
  delta?: number
  deltaSuffix?: string
  deltaInvert?: boolean
  series?: number[]
  accent?: boolean
  hint?: string
}) {
  return (
    <div className="flex flex-col gap-[var(--space-2)] rounded-[var(--radius-lg)] border border-[var(--border-subtle)] bg-[var(--surface-2)] p-[var(--space-4)]">
      <div className="flex items-center gap-[var(--space-1)]">
        <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">{label}</span>
        {hint ? <Hint text={hint} /> : null}
      </div>
      <span
        className={cn(
          'text-[length:var(--text-2xl)] font-semibold tabular-nums leading-none',
          accent ? 'text-[var(--accent)]' : 'text-[var(--text-primary)]',
        )}
      >
        {nf(value)}
      </span>
      {series && series.length > 1 ? (
        <span className="block w-full text-[var(--accent)]">
          <Sparkline values={series} height={30} ariaLabel={`${label} trend`} />
        </span>
      ) : null}
      <div className="flex items-center justify-between gap-[var(--space-2)] min-h-[var(--space-4)]">
        {delta !== undefined ? <Delta value={delta} invert={deltaInvert} suffix={deltaSuffix} /> : <span />}
        {sub ? <span className="text-[length:var(--text-xs)] text-[var(--text-muted)] truncate">{sub}</span> : null}
      </div>
    </div>
  )
}

function Trend({ label, series, invert, tone = 'accent' }: { label: string; series: number[]; invert?: boolean; tone?: 'accent' | 'danger' }) {
  const total = sum(series)
  const d = last7(series) - prev7(series)
  return (
    <div className="flex flex-col gap-[var(--space-1)] rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--surface-1)] p-[var(--space-3)]">
      <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">{label}</span>
      <span className="text-[length:var(--text-lg)] font-semibold tabular-nums leading-none text-[var(--text-primary)]">
        {nf(total)}
      </span>
      <span className={cn('block w-full', tone === 'danger' && total > 0 ? 'text-[var(--danger)]' : 'text-[var(--accent)]')}>
        <Sparkline values={series} height={26} ariaLabel={`${label} trend`} />
      </span>
      <Delta value={d} invert={invert} />
    </div>
  )
}

function Delta({ value, invert, suffix }: { value: number; invert?: boolean; suffix?: string }) {
  if (value === 0) {
    return (
      <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">
        no change{suffix ? ` ${suffix}` : ''}
      </span>
    )
  }
  const good = invert ? value < 0 : value > 0
  const Arrow = value > 0 ? ArrowUpRight : ArrowDownRight
  return (
    <span
      className={cn(
        'inline-flex items-center gap-[1px] text-[length:var(--text-xs)] font-medium tabular-nums',
        good ? 'text-[var(--accent-positive-fg)]' : 'text-[var(--accent-negative-fg)]',
      )}
    >
      <Arrow width={12} height={12} aria-hidden="true" />
      {value > 0 ? '+' : ''}
      {nf(value)}
      {suffix ? <span className="text-[var(--text-muted)] font-normal"> {suffix}</span> : null}
    </span>
  )
}

function Hint({ text }: { text: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          aria-label="What does this mean?"
          className="inline-flex text-[var(--text-muted)] hover:text-[var(--text-primary)]"
        >
          <Info width={12} height={12} aria-hidden="true" />
        </button>
      </TooltipTrigger>
      <TooltipContent className="max-w-[18rem]">{text}</TooltipContent>
    </Tooltip>
  )
}

function Panel({ title, empty, children }: { title: string; empty: string; children: React.ReactNode[] }) {
  return (
    <div className="flex flex-col gap-[var(--space-2)] rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--surface-1)] p-[var(--space-3)]">
      <span className="text-[length:var(--text-xs)] font-medium uppercase tracking-wide text-[var(--text-muted)]">
        {title}
      </span>
      {children.length > 0 ? (
        <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-2)]">{children}</ul>
      ) : (
        <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">{empty}</span>
      )}
    </div>
  )
}

function Health({ label, value, sub, warn }: { label: string; value: number; sub: string; warn: boolean }) {
  return (
    <div className="flex flex-col gap-[var(--space-1)] rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--surface-1)] p-[var(--space-3)]">
      <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">{label}</span>
      <span className={cn('text-[length:var(--text-xl)] font-semibold tabular-nums leading-none', warn ? 'text-[var(--warning)]' : 'text-[var(--text-primary)]')}>
        {nf(value)}
      </span>
      <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">{sub}</span>
    </div>
  )
}

// --- Row renderers ----------------------------------------------------------

const AVATAR_TONES: AvatarTone[] = [
  'collab-1', 'collab-2', 'collab-3', 'collab-4', 'collab-5', 'collab-6', 'collab-7', 'collab-8',
]
function avatarTone(name: string): AvatarTone {
  let h = 0
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0
  return AVATAR_TONES[h % AVATAR_TONES.length]
}
function initials(name: string): string {
  const parts = name.trim().split(/\s+/)
  const s = (parts[0]?.[0] ?? '') + (parts.length > 1 ? (parts[parts.length - 1]?.[0] ?? '') : '')
  return s.toUpperCase() || name.slice(0, 1).toUpperCase() || '?'
}

function Rank({ n }: { n: number }) {
  return <span className="w-[var(--space-4)] shrink-0 text-[length:var(--text-xs)] tabular-nums text-[var(--text-muted)]">{n}</span>
}

function Count({ children }: { children: React.ReactNode }) {
  return <span className="shrink-0 tabular-nums text-[length:var(--text-xs)] text-[var(--text-muted)]">{children}</span>
}

function PageRow({ p, rank }: { p: StatsTopPage; rank: number }) {
  return (
    <li className="flex items-center gap-[var(--space-2)] text-[length:var(--text-sm)]">
      <Rank n={rank} />
      <button
        type="button"
        onClick={() => navigateToPage(p.space_id, p.page_id)}
        className="reader-meta-link min-w-0 flex-1 truncate text-left"
      >
        {p.title} <span className="text-[var(--text-muted)]">· {p.space_name}</span>
      </button>
      <Count>{nf(p.count)}</Count>
    </li>
  )
}

function ContributorRow({ c, rank }: { c: StatsTopPerson; rank: number }) {
  return (
    <li className="flex items-center gap-[var(--space-2)] text-[length:var(--text-sm)]">
      <Rank n={rank} />
      <Avatar size="sm" tone={avatarTone(c.label)}>{initials(c.label)}</Avatar>
      <span className="min-w-0 flex-1 truncate font-medium text-[var(--text-primary)]">{c.label}</span>
      <Count>{nf(c.count)} edits</Count>
    </li>
  )
}

function SpaceRow({ s, rank }: { s: StatsTopSpace; rank: number }) {
  return (
    <li className="flex items-center gap-[var(--space-2)] text-[length:var(--text-sm)]">
      <Rank n={rank} />
      <span className="min-w-0 flex-1 truncate text-[var(--text-primary)]">{s.name}</span>
      <Count>{nf(s.count)}</Count>
    </li>
  )
}

function SignupRow({ u }: { u: StatsSignup }) {
  const name = u.display_name || u.username
  return (
    <li className="flex items-center gap-[var(--space-2)] text-[length:var(--text-sm)]">
      <Avatar size="sm" tone={avatarTone(name)}>{initials(name)}</Avatar>
      <div className="min-w-0 flex-1">
        <div className="truncate text-[var(--text-primary)]">{name}</div>
        {u.email ? <div className="truncate text-[length:var(--text-xs)] text-[var(--text-muted)]">{u.email}</div> : null}
      </div>
      <StatusBadge tone={u.activated ? 'positive' : 'neutral'}>
        {u.activated ? 'active' : 'no activity'}
      </StatusBadge>
      <Count>{relativeTimeFromSqlite(u.created_at)}</Count>
    </li>
  )
}

function UnansweredRow({ a }: { a: StatsUnanswered }) {
  return (
    <li className="flex flex-col gap-[1px] text-[length:var(--text-sm)]">
      <span className="text-[var(--text-primary)]">{a.question}</span>
      <span className="text-[length:var(--text-xs)] text-[var(--text-muted)]">
        {a.who ? `${a.who} · ` : ''}
        {relativeTimeFromSqlite(a.created_at)}
      </span>
    </li>
  )
}

// --- AI endpoints & reliability — per-service health + relief topology -------

function AIReliabilitySection() {
  const q = useAIEndpoints()
  if (q.isLoading || q.isError || !q.data) return null
  const d = q.data
  const configured = d.services.filter((s) => s.configured)
  if (configured.length === 0) {
    return (
      <Section
        title="AI endpoints & reliability"
        desc="The backing model services and whether they're reachable."
      >
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          No AI service is configured. Set <code>TELA_LLM_URL</code> and{' '}
          <code>TELA_RAG_EMBED_URL</code> to enable chat and embeddings — point them at a relief
          proxy (LiteLLM) for automatic failover when an endpoint is overloaded.
        </p>
      </Section>
    )
  }
  return (
    <Section
      title="AI endpoints & reliability"
      desc="Live reachability of each backing model service. Route them through a relief proxy (LiteLLM) so traffic fails over automatically when an endpoint clogs."
    >
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-[var(--space-3)]">
        {configured.map((s) => (
          <EndpointCard key={s.service} s={s} probed={d.probed} relief={d.relief_proxy} />
        ))}
      </div>
      {d.gate ? <GateStrip g={d.gate} /> : null}
      {d.grafana_url ? (
        <a
          href={d.grafana_url}
          target="_blank"
          rel="noreferrer"
          className="reader-meta-link mt-[var(--space-3)] inline-flex items-center gap-[var(--space-1)] text-[length:var(--text-sm)]"
        >
          Failover & per-endpoint detail in Grafana
          <ExternalLink width={13} height={13} aria-hidden="true" />
        </a>
      ) : null}
    </Section>
  )
}

function EndpointCard({ s, probed, relief }: { s: AIEndpointHealth; probed: boolean; relief: boolean }) {
  const label = s.service === 'embed' ? 'Embeddings' : 'Chat & generation'
  const tone = !probed ? 'neutral' : s.healthy ? 'positive' : 'negative'
  const statusText = !probed ? 'Checking…' : s.healthy ? 'Up' : 'Down'
  return (
    <div className="flex flex-col gap-[var(--space-2)] rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--surface-1)] p-[var(--space-3)]">
      <div className="flex items-center justify-between gap-[var(--space-2)]">
        <span className="text-[length:var(--text-sm)] font-medium text-[var(--text-primary)]">{label}</span>
        <StatusBadge tone={tone} dot>
          {statusText}
        </StatusBadge>
      </div>
      <div className="flex flex-wrap items-center gap-[var(--space-2)]">
        <span className="min-w-0 truncate text-[length:var(--text-xs)] text-[var(--text-muted)]" title={s.endpoint}>
          {s.endpoint || '—'}
        </span>
        {relief ? (
          <StatusBadge tone="info">relief pool</StatusBadge>
        ) : (
          <StatusBadge tone="neutral">single endpoint</StatusBadge>
        )}
      </div>
      <dl className="m-0 grid grid-cols-2 gap-x-[var(--space-3)] gap-y-[var(--space-1)]">
        <EndpointField label="Model" value={s.model || '(default)'} />
        <EndpointField label="Probe latency" value={probed ? `${s.latency_ms} ms` : '—'} />
        <EndpointField
          label={s.healthy ? 'Up for' : 'Down for'}
          value={s.since ? relativeTimeFromSqlite(s.since) : '—'}
        />
        <EndpointField label="Last reachable" value={s.last_ok ? relativeTimeFromSqlite(s.last_ok) : 'never'} />
      </dl>
      {probed && !s.healthy && s.reason ? (
        <p className="m-0 text-[length:var(--text-xs)] text-[var(--danger)] break-words">{s.reason}</p>
      ) : null}
    </div>
  )
}

// GateStrip — the foreground (ask/assist) concurrency gate: the cap before an
// overloaded primary spills to the relief layer, the live in-flight count, and
// how often it has spilled. Down-failover volume is NOT here (LiteLLM owns that —
// see the Grafana link); this is purely the overload gate.
function GateStrip({ g }: { g: AIGate }) {
  return (
    <div className="mt-[var(--space-3)] flex flex-col gap-[var(--space-2)] rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--surface-1)] p-[var(--space-3)]">
      <span className="text-[length:var(--text-sm)] font-medium text-[var(--text-primary)]">
        Foreground concurrency gate
      </span>
      <dl className="m-0 grid grid-cols-3 gap-x-[var(--space-3)] gap-y-[var(--space-1)]">
        <EndpointField label="Limit" value={String(g.limit)} />
        <EndpointField label="In flight" value={String(g.in_flight)} />
        <EndpointField
          label="Spilled to relief"
          value={g.overflow ? g.spills.toLocaleString() : 'no target'}
        />
      </dl>
      <p className="m-0 text-[length:var(--text-xs)] text-[var(--text-muted)]">
        {g.overflow
          ? 'Ask/assist cap on the primary; the surplus spills to the relief layer when saturated.'
          : 'Ask/assist cap on the primary; with no spill target a full gate queues rather than failing over.'}
      </p>
    </div>
  )
}

function EndpointField({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-[1px] min-w-0">
      <dt className="text-[length:var(--text-xs)] text-[var(--text-muted)]">{label}</dt>
      <dd className="m-0 truncate text-[length:var(--text-sm)] tabular-nums text-[var(--text-primary)]" title={value}>
        {value}
      </dd>
    </div>
  )
}

// --- AI usage (token volume) — loads independently of the main stats query --

function AIUsageSection() {
  const q = useAIUsage()
  if (q.isLoading || q.isError || !q.data) return null
  const { weeks, models } = q.data
  if (weeks.length === 0 && models.length === 0) {
    return (
      <Section title="AI usage · token volume">
        <p className="m-0 text-[length:var(--text-sm)] text-[var(--text-muted)]">
          No AI usage recorded yet — it accrues as chat, embedding, and image calls run.
        </p>
      </Section>
    )
  }
  return (
    <Section title="AI usage · token volume (estimated)" desc="Apply a per-provider price table to these for a cost estimate (~4 chars/token).">
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-[var(--space-4)]">
        <div className="flex flex-col gap-[var(--space-2)] rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--surface-1)] p-[var(--space-3)] overflow-x-auto">
          <span className="text-[length:var(--text-xs)] font-medium uppercase tracking-wide text-[var(--text-muted)]">
            By week
          </span>
          <table className="w-full text-[length:var(--text-sm)] tabular-nums">
            <thead>
              <tr className="text-[var(--text-muted)] text-left text-[length:var(--text-xs)]">
                <th className="font-medium pr-[var(--space-3)]">Week</th>
                <th className="font-medium pr-[var(--space-3)] text-right">Chat tok</th>
                <th className="font-medium pr-[var(--space-3)] text-right">Embed tok</th>
                <th className="font-medium text-right">Images</th>
              </tr>
            </thead>
            <tbody>
              {weeks.map((w) => (
                <tr key={w.week} className="text-[var(--text-primary)]">
                  <td className="pr-[var(--space-3)] text-[var(--text-muted)]">{w.week}</td>
                  <td className="pr-[var(--space-3)] text-right">{w.chat_tokens.toLocaleString()}</td>
                  <td className="pr-[var(--space-3)] text-right">{w.embed_tokens.toLocaleString()}</td>
                  <td className="text-right">{w.images.toLocaleString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="flex flex-col gap-[var(--space-2)] rounded-[var(--radius-md)] border border-[var(--border-subtle)] bg-[var(--surface-1)] p-[var(--space-3)]">
          <span className="text-[length:var(--text-xs)] font-medium uppercase tracking-wide text-[var(--text-muted)]">
            By model (12 weeks)
          </span>
          <ul className="m-0 p-0 list-none flex flex-col gap-[var(--space-1)]">
            {models.map((m) => (
              <li
                key={`${m.model}:${m.kind}`}
                className="flex items-baseline justify-between gap-[var(--space-2)] text-[length:var(--text-sm)]"
              >
                <span className="min-w-0 truncate text-[var(--text-primary)]">
                  <span className="font-medium">{m.model || '(default)'}</span>{' '}
                  <span className="text-[var(--text-muted)]">· {m.kind} · {m.calls.toLocaleString()} calls</span>
                </span>
                <span className="shrink-0 tabular-nums text-[var(--text-muted)]">
                  {(m.kind === 'image' ? m.units : m.tokens).toLocaleString()}
                </span>
              </li>
            ))}
          </ul>
        </div>
      </div>
    </Section>
  )
}

// --- Loading skeleton -------------------------------------------------------

function InsightsSkeleton() {
  return (
    <div className="flex flex-col gap-[var(--space-6)]" aria-busy="true" aria-live="polite">
      <span className="sr-only">Loading insights…</span>
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-[var(--space-3)]">
        {Array.from({ length: 4 }).map((_, i) => (
          <div
            key={i}
            className="h-[calc(var(--space-8)*2)] rounded-[var(--radius-lg)] border border-[var(--border-subtle)] bg-[var(--surface-2)] motion-safe:animate-pulse"
          />
        ))}
      </div>
      {Array.from({ length: 3 }).map((_, i) => (
        <div
          key={i}
          className="h-[calc(var(--space-8)*3)] rounded-[var(--radius-lg)] border border-[var(--border-subtle)] bg-[var(--surface-2)] motion-safe:animate-pulse"
        />
      ))}
    </div>
  )
}
