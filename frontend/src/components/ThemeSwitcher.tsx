import { useEffect, useState } from 'react'
import { Check, Moon, Sun, Sunset } from 'lucide-react'
import { ToggleGroup, ToggleGroupItem } from './ui/toggle'
import { Button } from './ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from './ui/dropdown-menu'
import {
  getTheme,
  setTheme,
  subscribeToTheme,
  THEMES,
  type ThemeName,
} from '../lib/theme'

const THEME_ICON: Record<ThemeName, typeof Sun> = {
  light: Sun,
  dark: Moon,
  warm: Sunset,
}

export function ThemeSwitcher() {
  const [active, setActive] = useState<ThemeName>(() => getTheme())

  // Keep the toggle group in sync with theme changes that originate elsewhere
  // (e.g., the "Toggle theme" command in the palette).
  useEffect(() => subscribeToTheme(setActive), [])

  const apply = (next: ThemeName) => {
    setTheme(next)
    setActive(next)
  }

  const ActiveIcon = THEME_ICON[active]

  return (
    <>
      {/* Three labelled segments need ~11rem — more than a phone header has to
          spare (it used to overflow the viewport edge), so small screens get an
          icon menu instead and the segmented control returns at md. */}
      <div className="md:hidden">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              aria-label={`Theme: ${active}`}
              className="h-[var(--space-8)] w-[var(--space-8)] p-0"
            >
              <ActiveIcon width={16} height={16} />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-[9rem]">
            {THEMES.map((name) => {
              const Icon = THEME_ICON[name]
              return (
                <DropdownMenuItem
                  key={name}
                  onSelect={() => apply(name)}
                  className="capitalize"
                >
                  <Icon width={14} height={14} />
                  {name}
                  {name === active ? (
                    <Check width={14} height={14} className="ml-auto" />
                  ) : null}
                </DropdownMenuItem>
              )
            })}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      <ToggleGroup
        type="single"
        value={active}
        onValueChange={(value) => {
          if (!value) return
          apply(value as ThemeName)
        }}
        aria-label="Theme"
        className="hidden md:flex"
      >
        {THEMES.map((name) => (
          <ToggleGroupItem key={name} value={name} className="capitalize">
            {name}
          </ToggleGroupItem>
        ))}
      </ToggleGroup>
    </>
  )
}
