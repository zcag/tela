import type { Options as RemarkMathOptions } from 'remark-math'

// One remark-math config for BOTH parsers (the view stack in `remark-stack.ts`
// and the editor's `mathRemarkPlugin`) — they must agree or the same body
// renders differently in read vs edit.
//
// `singleDollarTextMath: false` turns OFF `$…$` inline math. It is not a style
// preference: with it on, remark-math pairs the two `$` in a sentence like
// "your floor is **$3,500** … your current $2,500" into ONE inlineMath node,
// so the prose between them silently disappears from the rendered page (the
// markdown source still looks perfectly fine, and a lone `$` renders normally
// — so the edit that ADDS a second amount is what breaks text written weeks
// earlier). In a wiki, money is far more common than inline LaTeX.
//
// Inline math is `$$…$$` instead — mdast-util-math reads the same option, so
// inline math nodes serialize back as `$$…$$` and round-trip. Block math is
// unchanged (`$$` on its own lines).
export const MATH_OPTIONS: RemarkMathOptions = { singleDollarTextMath: false }
