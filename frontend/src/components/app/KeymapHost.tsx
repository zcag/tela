import { useMemo, useState } from 'react'
import { router } from '../../routes/router'
import { emitOpenPalette } from '../../lib/paletteEvent'
import { emitOpenNewPage } from '../../lib/newPageEvent'
import { emitToggleSidebar } from '../../lib/sidebarEvent'
import { cycleTheme } from '../../lib/theme'
import { useKeymap, type KeymapActions } from '../../lib/keys/useKeymap'
import { KeyCheatsheet } from '../ui/key-cheatsheet'
// Side-effect import: the bindings self-register on load. Keep it here so the
// registry is populated before the engine (and the cheatsheet) read it.
import '../../lib/keys/bindings'

// Mounts the app-wide vim-style keyboard layer + its `?` cheatsheet. Lives at
// the router root so every surface is covered — the authed shell, the
// ?view=read reader overlay, and the logged-out public/share readers. Surface
// gating (authed-only jumps vs everywhere motion/help) is resolved per keypress
// inside the engine, so one mount is correct for all of them.
//
// Distinct from AppCommandHost (which owns the ⌘K palette and only mounts in
// the authed layout): this host adds no queries, so it's safe at the root.
export function KeymapHost() {
  const [cheatsheetOpen, setCheatsheetOpen] = useState(false)

  const actions = useMemo<KeymapActions>(
    () => ({
      // `to` is a validated in-app path string; cast past the typed-route table
      // (same pattern as the /login `next` redirect).
      navigate: (to) => void router.navigate({ to: to as never }),
      openPalette: () => emitOpenPalette('pages'),
      openNewPage: () => emitOpenNewPage(),
      toggleTheme: () => cycleTheme(),
      toggleSidebar: () => emitToggleSidebar(),
      openCheatsheet: () => setCheatsheetOpen(true),
    }),
    [],
  )

  useKeymap(actions)

  return (
    <KeyCheatsheet open={cheatsheetOpen} onOpenChange={setCheatsheetOpen} />
  )
}
