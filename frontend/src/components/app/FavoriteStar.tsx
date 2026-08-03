import { Star } from 'lucide-react'
import { Button } from '../ui/button'
import { DropdownMenuItem } from '../ui/dropdown-menu'
import { cn } from '../../lib/utils'
import { useFavoriteStatus, useToggleFavorite } from '../../lib/queries/favorites'

// Header star toggle — stars/unstars the current page for the signed-in user.
// A ghost Button (not a Radix Toggle) so the *icon* fills when active, rather
// than the whole button getting a pressed-state background. Mirrors FollowButton.
//
// `asMenuItem` renders the same toggle as a labelled dropdown row instead: the
// mobile header has no room for a strip of icon buttons, so they fold into the
// "•••" overflow. Same hooks, same mutation — only the shell differs.
export function FavoriteStar({
  pageId,
  asMenuItem,
  className,
}: {
  pageId: number
  asMenuItem?: boolean
  className?: string
}) {
  const { data } = useFavoriteStatus(pageId)
  const toggle = useToggleFavorite()
  const isFavorited = data ?? false

  if (asMenuItem) {
    return (
      <DropdownMenuItem
        className={className}
        disabled={toggle.isPending}
        onSelect={() => toggle.mutate({ pageId, isFavorited })}
      >
        <Star
          width={14}
          height={14}
          className={isFavorited ? 'fill-[var(--accent)] text-[var(--accent)]' : undefined}
        />
        {isFavorited ? 'Remove from favorites' : 'Add to favorites'}
      </DropdownMenuItem>
    )
  }

  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      onClick={() => toggle.mutate({ pageId, isFavorited })}
      disabled={toggle.isPending}
      aria-label={isFavorited ? 'Remove from favorites' : 'Add to favorites'}
      title={isFavorited ? 'Remove from favorites' : 'Add to favorites'}
      className={cn('h-[var(--space-8)] w-[var(--space-8)] p-0', className)}
    >
      <Star
        width={16}
        height={16}
        className={isFavorited ? 'fill-[var(--accent)] text-[var(--accent)]' : undefined}
      />
    </Button>
  )
}
