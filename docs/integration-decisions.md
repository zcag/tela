# Integration decisions — where a new capability lives

When building a new capability *around* tela (a tool, an agent, a pipeline), the
recurring question is: does it live inside tela, beside it, or plug into one
another? This is the rubric. It exists to stop re-litigating the same decision
per feature.

## The option space is a 2×2

Two axes: **who hosts** (tela vs the new tool `x`) × **direct or via a plugin
framework**.

|                     | Direct / native                                   | Via a plugin framework                          |
|---------------------|---------------------------------------------------|-------------------------------------------------|
| **tela hosts**      | **4.** Build `x` natively in tela (MCP tool + `xCore`) — *the atlas way* | **1.** `x`-as-tela-plugin → add plugin func to **tela** first |
| **`x` hosts** (standalone) | **3.** `x` calls tela's API/MCP directly           | **2.** tela-as-`x`-plugin → add plugin func to **`x`** first |

The column is whether you pay for a **plugin framework**; the row is which side
is the **host**.

## The gates (apply in order)

1. **Plugin frameworks are platform bets, not integration choices.** A plugin
   framework is justified by the *Nth* integration, never the first (rule of
   three). Building one to hold a single guest is over-engineering dressed as
   architecture. So **options 1 and 2 are off the table by default** — pick them
   only when you deliberately intend a platform, not as a home for one feature.
   - tela already has its plugin seam: **MCP tools + `xCore`**. Don't replace it
     with a heavier framework speculatively. Add one only when ≥3 queued
     features all need to attach in a way MCP can't express.

2. **Data gravity decides host (choosing between 3 and 4).** Look at the
   capability's primary **input** and **output**:
   - Both are wiki content, and tela is the only consumer → **4 (native in tela)**.
   - Input is external (web, files, other systems) → **3 (standalone, calls tela)**.
   The output landing as a tela page is *not* a reason to live inside tela — that's
   one API call.

3. **Multi-source hub is the one thing that flips 3 → 2.** If `x` genuinely
   integrates *many* knowledge backends (web + wiki + Notion + files…), then `x`
   needs a connector abstraction anyway and tela is one connector. Build the
   connector *interface* at the second source; a full plugin framework only when
   the third+ forces it.

4. **Default to the loosest coupling that ships; tighten on proven usage.**
   Loose→tight is a cheap refactor across the MCP seam. Tight→loose means
   un-baking from the monolith — expensive, so you stall before starting. Bias
   toward the reversible direction.

## Worked examples

- **atlas** (audits/coverage/gaps *of the wiki* → results *about the wiki*):
  both input and output are wiki content, tela is the only consumer → **4**,
  shipped as MCP tools (`atlas_run`, …). Correct.
- **KCE / perplexity-style research tool** (input is the web/external sources,
  output optionally lands as pages): external input → **3**. Flips to **2** only
  if built as a deliberate multi-source research hub.

## The meta-rule

Stop treating "plugin" as an integration option. It's a platform question with a
rule-of-three gate. Answer "one backend or many?" and the plugin options resolve
themselves — leaving you choosing between **3** and **4** on data gravity alone.
