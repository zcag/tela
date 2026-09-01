# atlas — output-budget changeset (EXPERIMENT, 2026-09-01)

> ⚠️ **This is a marked, self-contained TEST changeset.** It changes how atlas
> generation spends the model's output budget. Everything in it landed as one
> commit so it can be undone with a single `git revert <sha>` — there is no
> schema change, no migration, and no config to unwind. Generated pages are
> rewritten wholesale on every run, so reverting and re-running restores the
> previous output exactly.
>
> Grep marker: `EXPERIMENT 2026-09-01 (atlas output-budget pack)`.

## Why

The pipeline is calibrated, and the calibration is real — but it was done
entirely inside one regime. Every baseline in `docs/atlas.md` has a small
must-cover surface (compass 35 items, udn 17, COM 15, nest 20, radius\* 1–4), and
`cmd/atlasdiff` is a **port-fidelity** gate: it diffs a new space against a
reference space produced by the old system *on the same source*. Neither can see
a failure that only appears on a large surface.

Production history says the same thing. Across all 53 runs with a recorded
coverage, the repair loop has engaged on exactly **two** sources ever (repowise,
SerpApi) — every other run reached ≥95% must-cover at `validate` and returned at
the threshold check without doing anything.

**What actually goes wrong at scale: reference pages are silently truncated at
the model's output cap.** `TELA_LLM_MAX_TOKENS=4096` in prod. On run 133
(SerpApi), the five reference pages' raw output measured ≈615 / 1,513 / 1,586 /
**4,462** / **4,791** tokens — and the two sitting at the cap are the two that end
mid-word (one stops inside `` ### `--save-interval` ( ``). The loss is a perfect
alphabetical tail:

```
--safe             present
--save-interval    present   ← body cuts off mid-line here
--scan-fingerprints  ABSENT
... 19 consecutive flags absent
```

That page also contains **0 of its 36 environment variables** — it has no
`## Environment` section at all, because it died inside the flag list. Per-page
enumeration completeness decays monotonically with surface size (1–5 items 100%,
138 routes 97%, 118 items 53%, 153 items 31%), which is the truncation
fingerprint, not a documentation-quality signal.

The root cause is a **budget bounded on the wrong side of the call**.
`refListBudget` capped the *input* item list (added for a real reason — a 456 KB
prompt OOM'd the model) while nothing bounded the *output*. So the prompt handed
the model up to 32,000 characters of items and instructed it to document EVERY
one — a list it cannot even echo inside a 4,096-token answer. The audit then
reported the cut-off tail as a *documentation gap*, and repair "fixed" the gap by
re-issuing the same impossible request.

That also explains repair's behaviour, which is otherwise mysterious. Coverage of
a reference page measures *how far down the alphabet the model got before the
cap*; repair regenerates the page from scratch (it never receives the previous
body), so each pass re-rolls where the cut falls. The `CRITICAL: these items MUST
appear` preamble makes the model more verbose early, which moves the cut
*earlier* — a feedback loop into worse truncation (SerpApi's gaps went 33 → 64 →
74). Measured must-covered deltas on the only four runs where repair moved the
number at all: **+27, +18, −72, −41**.

## What changed

1. **Reference batches are sized by what a call can ANSWER** (`refBatchPlan`,
   `stage_draft.go`), not by what it can be told. The input char budget stays as
   the OOM guard; the output budget is now what decides items-per-part. A surface
   an order of magnitude too large switches from a section per item to a **table**
   (`compact`), which is both denser and the better shape for a list that long —
   this bounds cost rather than dropping items.
   *Fidelity:* every calibrated baseline plans to a single rich part, i.e. the
   byte-identical single-call prompt they always got. `TestRefBatchPlan_LeavesCalibratedBaselinesAlone`
   pins that.

2. **`finish_reason` is read instead of discarded** (`llm/client.go` →
   `ChatFull`, `chatBody`). A part cut off at `length` is a *defect*, not a gap:
   the reference path halves the batch and retries (bounded by
   `refTruncRetries`), which makes the budget in (1) self-correcting rather than
   a tuned constant. `refine` and the mermaid repair now **keep the existing
   page** when their reply is truncated — both replace a complete page, and the
   existing shrink guard does not catch a reply truncated at 60%.

3. **Reference items are joined to their own source deterministically**
   (`evidence.go`). Previously the grounding came from the hybrid retriever with a
   query built by concatenating *every item name in the batch* — a bag of 160 flag
   names describes none of them, so the model had each item's name and no code and
   wrote filler that restated the name (`--dev — "Enables dev mode."`). Every spine
   item already carries its exact `file:line` and every chunk carries its line
   range, so the join is exact and free; it also removes an embedding call per
   batch.
   *Scope:* code paths only. **Jira spine items carry a synthetic `file:line`**
   (`Line` is the item's ordinal within `schema.md`/`status.md`), so the tracker
   path keeps the retrieved-excerpt grounding it has always used — otherwise the
   join would staple unrelated lines onto every item.

4. **The repair loop is a ratchet** (`stage_repair.go`). A pass is kept only if
   must-coverage improved; a pass that loses coverage is rolled back — in memory
   *and* in the store — and stops the loop, and a pass that changes nothing stops
   it too (`repairThreshold = 0.95` is unreachable from most real starting points,
   so the loop otherwise always burns all three passes to arrive back where it
   began; that cost 6m10s on run 133). `refineStage` has had this instinct all
   along in its shrink guard; repair overwrote unconditionally.
   Repair also now shares the draft stage's renderer (`renderReferenceBody`)
   instead of keeping a second copy of the batching — the two had already drifted
   once, and a page should not change shape when it is repaired.

## What was deliberately NOT done

- **The coverage metric is unchanged.** It is still substring presence
  (`stage_validate.go:43`), which is satisfiable by a name-dump. Tightening it
  (requiring the item's citation near its name, per-page reporting) is the right
  next step, but doing it *before* the truncation fix would crater the headline
  number while the docs improved, and that reads as a regression caused by the
  fix. Fix truncation → re-measure → then tighten.
- **Repair was not made additive.** With truncation fixed, a regenerated page
  should no longer lose items, so the acceptance test is the safety property that
  matters; restructuring repair to append is a larger change to stack on top of an
  unverified fix.
- **No new benchmark baseline.** The standing set should gain a large-surface
  source (repowise or SerpApi) with expected *per-page* coverage, or this class of
  defect recurs. That is an ops change, not a code one.

## How to evaluate it

Re-run SerpApi (project 19 / source 24) and compare against run 133:

- **No reference page may end mid-item.** This is the primary check — it is what
  every other number here is downstream of.
- `Entry Points, Flags & Environment` must contain an `## Environment` section and
  all 36 env vars (was 0).
- Per-page enumeration completeness should stop decaying with surface size.
- Watch the run event log for `hit the model's output cap` warnings: a few mean
  the retry is doing its job; many mean `refRichItemChars` is underestimating real
  output cost and should be raised.
- Expect **more draft calls** — SerpApi's reference pages go from 5 single-part
  calls to ~11 — offset by repair no longer burning three full passes. On a very
  large surface (repowise, 3,817 exports) compact mode is what keeps this bounded;
  if run minutes or cost quotas trip, `refMaxRichParts` is the dial.

## Revert

`git revert <sha>` of the single commit. No migration, no config, no persisted
state depends on it; the next run regenerates every page from scratch.
