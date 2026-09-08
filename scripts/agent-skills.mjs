#!/usr/bin/env node
// Publishes tela's Claude-Code skill at /.well-known/agent-skills/ so an agent
// can find it without being told the plugin exists (Cloudflare's agent-skills
// discovery RFC, v0.2.0 — schemas.agentskills.io; consumed by `npx skills`).
//
// The skill's SOURCE stays where it already lives, `plugin/.../SKILL.md`. This
// copies it into the landing's public tree and writes the index, because the
// index entry carries a **content digest**: agent-skills clients verify the
// sha256 before installing, so a stale digest doesn't degrade — it hard-fails
// the install. Hand-maintaining that is a trap, hence `--check` in `make test`
// alongside blocks-gate, which is the same shape of problem (a generated copy
// of an upstream file that must not silently drift).
//
//   node scripts/agent-skills.mjs --write   # regenerate (make skills-gen)
//   node scripts/agent-skills.mjs --check   # fail if stale (make skills-gate)

import { createHash } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = join(dirname(fileURLToPath(import.meta.url)), '..');
const wellKnown = join(repo, 'landing/public/.well-known/agent-skills');
const indexPath = join(wellKnown, 'index.json');

// One entry per published skill: where the source lives, and the name the
// served copy takes. Add a row here when tela ships another skill.
const SKILLS = [{ name: 'tela-wiki', source: 'plugin/plugins/tela/skills/tela-wiki/SKILL.md' }];

// The `description` an agent matches on is the skill's own frontmatter
// description — duplicating it by hand into the index is how the two drift.
function frontmatterDescription(md, name) {
  const fm = /^---\n([\s\S]*?)\n---/.exec(md);
  const desc = fm && /^description:[ \t]*(.+)$/m.exec(fm[1]);
  if (!desc) throw new Error(`${name}: SKILL.md has no frontmatter 'description:'`);
  return desc[1].trim().replace(/^["']|["']$/g, '');
}

const files = new Map();
const skills = SKILLS.map(({ name, source }) => {
  const md = readFileSync(join(repo, source), 'utf8');
  files.set(join(wellKnown, name, 'SKILL.md'), md);
  return {
    name,
    type: 'skill-md',
    description: frontmatterDescription(md, name),
    // Relative, so a self-hosted instance serves its own copy rather than
    // pointing every installer at telawiki.com.
    url: `/.well-known/agent-skills/${name}/SKILL.md`,
    digest: `sha256:${createHash('sha256').update(md).digest('hex')}`,
  };
});

files.set(
  indexPath,
  JSON.stringify({ $schema: 'https://schemas.agentskills.io/discovery/0.2.0/schema.json', skills }, null, 2) + '\n',
);

const check = process.argv.includes('--check');
const stale = [];
for (const [path, want] of files) {
  let got = null;
  try {
    got = readFileSync(path, 'utf8');
  } catch {}
  if (got === want) continue;
  if (check) {
    stale.push(path.slice(repo.length + 1));
    continue;
  }
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, want);
  console.log(`wrote ${path.slice(repo.length + 1)}`);
}

if (stale.length) {
  console.error(`agent-skills: stale, run \`make skills-gen\`:\n  ${stale.join('\n  ')}`);
  process.exit(1);
}
if (check) console.log('agent-skills: up to date');
