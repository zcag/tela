// Module-scoped registry of application-level commands surfaced in the `>`
// (commands) mode of the Command palette. Future tasks add commands by calling
// `registerCommand` at module load time (see commands/starters.tsx).
//
// A command's `run` receives a CommandContext that the host materialises at
// render time — gives commands access to theme, router, query cache, and
// palette controls without each command importing them directly.

import type { ReactNode } from 'react'
import type { CommandItem } from '../components/ui/command'
import type { Space } from './types'

// What a command does, expressed in terms of its environment. Stays small on
// purpose — extend deliberately as new starter commands need new affordances.
export interface CommandContext {
  spaces: Space[]
  navigateToSpace: (spaceId: number) => void
  // Switches the open palette into help mode without closing it.
  openHelpMode: () => void
  // Swaps the palette body for an inline sub-picker. Selecting closes the
  // palette; Esc closes the palette (no back-to-commands in v0).
  openSubPicker: (spec: SubPickerSpec) => void
  // Imperatively close the palette (e.g., after a command finishes its work).
  closePalette: () => void
}

export interface SubPickerSpec {
  label: string
  placeholder: string
  emptyMessage?: string
  items: CommandItem[]
}

export interface CommandDefinition {
  id: string
  title: string
  subtitle?: string
  icon?: ReactNode
  keywords?: string[]
  // True for commands that don't close the palette themselves (e.g., commands
  // that switch into help mode or open a sub-picker). Most run-and-done
  // commands leave this false so the host closes the palette after they fire.
  keepPaletteOpen?: boolean
  run: (ctx: CommandContext) => void
}

// Keyed by id so HMR and accidental double-imports overwrite cleanly instead
// of producing duplicates.
const registry = new Map<string, CommandDefinition>()

export function registerCommand(cmd: CommandDefinition): void {
  registry.set(cmd.id, cmd)
}

export function getRegisteredCommands(): CommandDefinition[] {
  return Array.from(registry.values())
}

export function materializeCommands(ctx: CommandContext): CommandItem[] {
  return getRegisteredCommands().map((def) => ({
    id: def.id,
    title: def.title,
    subtitle: def.subtitle,
    icon: def.icon,
    keywords: def.keywords,
    keepPaletteOpen: def.keepPaletteOpen,
    onSelect: () => def.run(ctx),
  }))
}
