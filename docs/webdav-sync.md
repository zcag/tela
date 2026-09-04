# WebDAV file-sync (rclone)

tela exposes every space you can access as a **WebDAV** tree at `/dav`, so you
can two-way-sync your wiki to local markdown files with stock
[`rclone`](https://rclone.org) (or mount it with Finder / Windows Explorer / any
WebDAV client). This is the dogfood path of the sync feature (spec §9); a
tela-own engine with push + in-app conflict UX comes later.

> **Easiest setup: Settings → Sync → Connect a vault.** It mints a sync token and
> shows the exact `rclone config create` + sync commands to paste (token included,
> `vendor=rclone` and `--ignore-size` baked in, Linux and macOS variants). The
> rest of this doc is the manual reference.

- **Endpoint:** `https://telawiki.com/dav/`
- **Auth:** a **Personal Access Token (PAT)** as the password (any username).
  Mint one in **Settings → Sync** (or under API keys). Use a **write**-scope key
  to sync up, **read** to sync down only. A **space-pinned** key exposes just
  that one space.
- **Layout:** the root lists each space as a folder (`<space-slug>/`); inside,
  pages are `<slug>.md`. A page that has children is *also* a `<slug>/` folder
  holding them (the sibling-folder layout — identical to `export.zip`).

```
/dav/
  engineering/
    onboarding.md          ← a page
    onboarding/            ← its child pages
      laptop-setup.md
    roadmap.md
  personal/
    journal.md
```

## How identity works (read this once)

Every file carries an `id:` in its YAML frontmatter — that, **not the path**, is
the page's identity. So you can rename or move a file and tela rebinds the same
page (a retitle / reparent), instead of creating a duplicate. A brand-new file
(no `id:`) creates a page and gets an id assigned on the server; that id rides
back into the file on your next sync-down.

The body is pure markdown; the frontmatter (`id`, `title`, `slug`, `created`,
`updated`, plus any custom keys) is a generated view. Re-uploading a file tela
just gave you is a **no-op** — no churn, no duplicate.

**The on-disk filename is yours and stable.** A file *you* create over sync keeps
the name you gave it — tela records that name server-side and serves the page back
at it. So creating `meeting-notes.md` with a title of "Q3 Planning" stays
`meeting-notes.md` (not `q3-planning.md`), and renaming a file (`mv a.md b.md`)
sticks. This is what lets a brand-new file round-trip cleanly: the page comes back
at the exact path you wrote, so the client's post-write check passes instead of
chasing a different name. The URL `slug` in the frontmatter still follows the
**title** — it's the page's web address, kept separate from the on-disk filename.
(Pages created in the app are named by their title-slug until a sync client first
creates or renames them.)

## Non-markdown files

A vault isn't only markdown. Drop a `report.pdf`, an image, a `.csv` — anything
that isn't `.md`/`.markdown` — anywhere in the tree and it **syncs as a file**
(stored as a `space_file`, migration `0015`), so it reaches your other machines
like any other vault content. Markdown is still a page; everything else is a
file. The two never collide (different namespaces, keyed differently).

- **Identity is the path, not an id.** A raw file has no frontmatter to carry an
  id, so its identity is its location: the space, the folder page it sits under,
  and its name. Renaming/moving a *page* keeps its files attached (they hang off
  the parent page, not a frozen path string).
- **Conflicts are last-write-wins** by content (no 3-way merge — that's
  markdown-only). Re-uploading identical bytes is a no-op (content-addressed, so
  no churn); different bytes replace in place.
- **Deletes are soft** and gated by the same mass-delete brake pages get, so a
  wiped local vault can't erase a space's files in one run — and anything deleted
  is recoverable.
- **Size cap:** `TELA_WEBDAV_FILE_MAX_BYTES` (default **50 MiB**) per file. Blobs
  live in Postgres, so this bloats the DB/backups — it's "carry your
  attachments", not a Dropbox replacement. An oversized PUT **fails loudly**
  (it is not silently dropped). OS/editor junk (`.DS_Store`, `._*`, `*.swp`,
  `*.tmp`, `Thumbs.db`) is still accept-and-dropped.
- **Surfaced as page attachments.** A file synced into a page's folder shows up
  in that page's **Attachments** strip (below the title, reader + editor) — no
  body edit. It's served at the public, content-addressed `/api/files/{space}/{hash}`
  (images inline, everything else forced-download). Drag/paste a file into the
  editor to upload + embed inline (images as `![]()`, others as a `:::file` card);
  image uploads share this same store. See `docs/attachments.md`.

### Conflict handling — server-side 3-way merge

tela merges on the server. When a page changed both in the app and in your local
file since your last sync, **non-overlapping edits combine automatically** (you
edited the intro, a teammate edited the footer → both land). Edits to the **same
lines** are a conflict: your local edit wins the visible copy, the page is
flagged for review, and the overridden server version is kept as a revision
(`source = sync-conflict`) — nothing is ever lost. Merge is line-based, so
edits on adjacent lines may be treated as one conflicting block.

The merge anchors on runs of lines that **both** sides left untouched, and slides
both alignments to a canonical position first. That matters for markdown, which
is full of blank lines and near-identical bullets: with only one side's diff
boundaries to go on, an edit inside such a run can be attributed to the wrong
place and the merge then drops or duplicates a line **without reporting a
conflict**. That was the behaviour until 2026-09-04. A residue of genuine
ambiguity remains — when both sides edit the same run of identical lines, where
the surviving copies sit is not determined — but it no longer changes which
lines survive.

Merging is independent of how your client *shapes* the write: `rclone bisync
--conflict-loser delete` resolves a both-sides edit as DELETE(loser) +
PUT(winner), which lands on the resurrect path (the file carries the page's
`id:`, so the trashed page comes back) — that merges too. It did not until
2026-09-01: it applied the incoming file blind, so that flag combination turned
the documented merge into silent last-write-wins.

⚠️ **A page with sub-pages is `<page>.md` NEXT TO a `<page>/` directory, and a
delete of the file takes the directory's pages with it** — deleting a page
deletes its subtree, and the sync surface has no way to say "just this one". So
that DELETE(loser) + PUT(winner) pair used to trash every sub-page and bring
back only the parent — silently, and at the time nothing listed a deleted page,
so there was nowhere to notice it (a space's **Trash** tab now shows them, and
restores one with its sub-pages). Since
2026-09-04 a delete records which delete took each row (`pages.deleted_root_id`,
migration 0080) and the resurrect restores that whole set, so the pair is
lossless again. A sub-page you deleted **on its own** carries a different root
and stays deleted. Pages trashed before 0080 have no root recorded and still
resurrect alone.

> **First-edit caveat:** the merge needs a *base* (what your client last sent).
> A page created in the app and edited locally **before your client has ever
> uploaded it** has no base yet, so that first write is last-write-wins. After
> the client has uploaded a page once, edits merge.

## ⚠️ Two client settings are load-bearing

Get either of these wrong and rclone still exits 0 — it just stops syncing your
edits. Both are baked into what **Settings → Sync** generates.

### 1. `vendor = rclone` on the remote

This is the rclone profile for a WebDAV server that accepts an upload
modification time (`X-OC-Mtime`) — which tela does. With it, rclone reports
`Precision: 1s` and compares modtimes. With `vendor = other` it reports
`ModTimeNotSupported` and **ignores modtime entirely**.

That matters because of the next setting. Fix an existing remote with:

```bash
rclone config update tela vendor rclone
rclone backend features tela: | head -5   # Precision must NOT be 3153600000000000000
```

### 2. `--ignore-size` on everything that writes

tela **transforms files on write** — it renders the YAML frontmatter (id,
title, …) and may merge your edit with the server's. So the bytes stored differ
from the bytes you uploaded, and rclone's default post-transfer **size check
will call every upload "corrupted" and roll it back** (it can even delete the
page).

```bash
rclone copy   ./engineering tela:engineering --ignore-size
rclone bisync ./engineering tela:engineering --ignore-size ...
```

### Why the pair is the whole story

rclone decides whether to transfer a file with `equal()`: **size, then
modtime**. `--ignore-size` removes the first signal on purpose. If the remote
also has no modtime, *nothing is left* — and `equal()` short-circuits on
"Sizes identical" and calls every already-existing file unchanged:

```
DEBUG : notes.md: Sizes identical
DEBUG : notes.md: Unchanged skipping
```

That was the shape of a real data-loss bug (fixed 2026-09-01, migration `0079`):
new files uploaded fine, but **every edit to an already-synced page was silently
dropped**. bisync detected the change and queued the copy correctly — the
delegated copy refused it — and the run still printed `Bisync successful` and
advanced its listings, so it was never retried. With `vendor = rclone` both
sides have a real modtime and the comparison is correct again.

**How tela's modtime works** (`backend/internal/api/dav_mtime.go`): a PUT
carrying `X-OC-Mtime` keeps your file's own timestamp — but only when your file
is a faithful copy of what tela serves (byte-identical, or an unchanged re-PUT
of a file that already carries the page's `id:`). When tela transformed the
write — a brand-new page getting its `id:`, a 3-way merge, a rename — the
page's own `updated_at` stands instead, which is newer than your local file, so
your next sync **pulls the canonical rendering down**. That is how a
locally-created file learns its `id:` (see [How identity works](#how-identity-works-read-this-once))
and how a merged body reaches the machine that wrote half of it.

## rclone config

`rclone config` → `n` (new remote) → name it `tela` → `webdav`, then:

```ini
# ~/.config/rclone/rclone.conf
[tela]
type = webdav
url = https://telawiki.com/dav/
vendor = rclone           # load-bearing — see above
user = you@example.com
pass = <obscured PAT>     # set via: rclone obscure 'tela_pat_xxx'
```

Generate the obscured password with `rclone obscure 'tela_pat_xxx'` (rclone
stores passwords obscured, not plain).

### Sync down (one space → a local folder)

```bash
rclone sync tela:engineering ./engineering --create-empty-src-dirs
```

### Two-way sync (bisync)

First run establishes the baseline; subsequent runs reconcile both directions:

```bash
# one-time baseline
rclone bisync tela:engineering ./engineering --resync --ignore-size

# ongoing (cron / systemd timer)
rclone bisync tela:engineering ./engineering \
  --ignore-size --max-delete 25 --check-access \
  --exclude-from ~/.config/rclone/tela-excludes.txt

# …then bring tela's rendering back down (ids, merges, frontmatter)
rclone copy tela:engineering ./engineering --update
```

`--max-delete` is a safety rail (refuse a run that would delete an anomalous
fraction of files); `--check-access` aborts if a side looks empty/unmounted.

**Why the trailing `copy --update`.** bisync re-lists both sides at the end of a
run, so a file tela rewrote *during* that run (a new page getting its `id:`, a
merge) looks unchanged to the next run and never comes down on its own. The pull
pass fixes that: `--update` only takes files whose server copy is newer, so it is
a no-op once everything has settled, and the following bisync run sees matching
modtimes and transfers nothing. Note it does **not** pass `--ignore-size` — a
download is a straight copy, so the size check is a real integrity check there;
keep it.

Without that pass a locally-created file keeps a body with no `id:` — and since
the id is identity, a later rename forks a new page instead of retitling it.

### Mount as an always-on folder (recommended)

A live `rclone mount` is the simplest "always there" setup — the vault shows up
as a normal folder; edits go straight up (the server merges), server-side
changes appear after `--dir-cache-time`. This is what **Settings → Sync**
generates (including a ready systemd user service).

```bash
rclone mount tela: ~/tela \
  --vfs-cache-mode full --dir-cache-time 10s --vfs-write-back 2s --ignore-size
```

`--ignore-size` is **required** (same reason as above — tela rewrites frontmatter
on write); leave modtime ON (don't pass `--no-modtime`) so server-side edits are
detected via `getlastmodified` (← `updated_at`). `--vfs-cache-mode full` gives a
real local cache so editors and offline reads work. New files you create get
their `id:` frontmatter on the next refresh. WebDAV has no change-notification,
so lower `--dir-cache-time` for fresher reads at the cost of more PROPFINDs.

Make it permanent with a **systemd user service** (mounts on login, restarts on
failure) — `~/.config/systemd/user/tela-vault.service`:

```ini
[Unit]
Description=tela vault — rclone mount of tela:
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStartPre=/usr/bin/mkdir -p %h/tela
ExecStartPre=-/usr/bin/fusermount3 -uz %h/tela
ExecStart=/usr/bin/rclone mount tela: %h/tela --vfs-cache-mode full --dir-cache-time 10s --vfs-write-back 2s --ignore-size
ExecStop=/usr/bin/fusermount3 -uz %h/tela
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload && systemctl --user enable --now tela-vault.service
```

Prefer real local files synced periodically instead of a live mount? Use the
**bisync** recipe above on a cron / systemd timer. Sub-second end-to-end is the
job of the future push-based engine, not rclone.

### Mount on macOS (`nfsmount`, no macFUSE)

macOS has no FUSE: macFUSE is a kernel extension that needs Reduced Security
and a reboot, so `rclone mount` is a non-starter for most people. `rclone
nfsmount` serves the same VFS over a loopback NFS mount — no kext, no root, same
flags:

```bash
rclone nfsmount tela: ~/tela \
  --vfs-cache-mode full --dir-cache-time 10s --vfs-write-back 2s --ignore-size
```

Keep it mounted with a **launchd agent** —
`~/Library/LaunchAgents/com.telawiki.tela-vault.plist`. It runs under `/bin/sh`
so `$HOME` expands on the machine (a plist has no systemd-style `%h`), and sets
`PATH` explicitly because a launchd agent inherits none — Homebrew is
`/opt/homebrew/bin` (Apple silicon) or `/usr/local/bin` (Intel):

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.telawiki.tela-vault</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/sh</string>
    <string>-c</string>
    <string>mkdir -p "$HOME/tela" &amp;&amp; exec rclone nfsmount tela: "$HOME/tela" --vfs-cache-mode full --dir-cache-time 10s --vfs-write-back 2s --ignore-size</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardErrorPath</key><string>/tmp/com.telawiki.tela-vault.log</string>
</dict>
</plist>
```

```bash
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.telawiki.tela-vault.plist
launchctl print gui/$(id -u)/com.telawiki.tela-vault | head
```

**Settings → Sync** generates both variants (Linux/macOS tabs) with your token
and paths filled in. Windows: rclone + WinFsp as a service.

### Excludes (`tela-excludes.txt`)

Keep OS/editor junk out of your wiki (tela also drops these server-side, but
filtering on the client saves round-trips):

```
.DS_Store
._*
*.swp
*.tmp
~$*
Thumbs.db
.git/**
.obsidian/**
```

## Notes & limits

- **Modtime.** tela accepts an upload modtime (`X-OC-Mtime`, hence
  `vendor = rclone`) and reports it back as `getlastmodified`; without a stamp
  the page's `updated_at` is reported. It also serves a strong `ETag` (page id +
  version) so unchanged files are skipped without re-hashing. See
  [Two client settings are load-bearing](#-two-client-settings-are-load-bearing).
- **Renames over `rclone bisync`** show up as delete + re-upload; because the
  re-uploaded file keeps its `id:`, tela rebinds the same page (and stamps the
  new filename) rather than forking it. The delete then lands on a path that no
  longer resolves — tela answers `204` there rather than `404`, because a 404
  makes rclone count a failed delete and bisync abort the whole run with
  "critical error … must run --resync". Deleting nothing is idempotent; the
  delete guards below still apply to paths that DO resolve.
- **Renaming a `.md` file** (e.g. in a mounted client) retitles the page;
  renaming a folder reparents it. Editing only the `slug:` in frontmatter is
  ignored — the slug is always derived from the title.
- **Creating a space:** `mkdir ~/tela/<folder>` at the **root** mints a new space
  owned by you (a root-level MKCOL). The folder name is the slug verbatim when
  it's already slug-valid, so it round-trips at the exact name; otherwise a slug
  is derived. A **space-pinned** PAT can't create spaces, and the whole behaviour
  is disabled with `TELA_WEBDAV_CREATE_SPACES=0` (then spaces are in-app only).
  Deletion is intentionally asymmetric — you can create a space by `mkdir`, but a
  space DELETE/`rmdir` over WebDAV is always refused (delete it in-app).
- **Viewers** (read-only space role) get a read-only tree; `PUT`/`MKCOL`/`DELETE`
  return 403.
- **Delete-safety.** Two server-side guards back up rclone's `--max-delete`
  (deletes are soft / recoverable regardless): a client may only delete a page it
  has **previously synced** (so a partial or fresh client can't remove pages it
  never pulled), and a **mass-delete brake** refuses once an anomalous fraction
  of a space would vanish in a short window — tunable via
  `TELA_WEBDAV_DELETE_FLOOR` (default 20, always-allowed per window) and
  `TELA_WEBDAV_DELETE_FRACTION` (default 0.5). A refused delete returns 405.
- **Disable** the surface entirely with `TELA_WEBDAV_ENABLED=0`.
- **Cloudflare WAF:** the non-standard WebDAV verbs (PROPFIND/MKCOL/MOVE/…) must
  be allowed through to `/dav/*` at the edge; some managed WAF profiles block
  them by default.
