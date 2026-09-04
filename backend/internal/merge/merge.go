// Package merge implements the server-side three-way merge that is the keystone
// of tela's file sync (sync spec §5). Body text merges line-based (markdown is
// line-oriented); props merge field-by-field; a title merges as a scalar. Each
// returns the merged value plus whatever truly conflicted, so the caller can
// keep both sides as revisions and flag the page — it never writes <<< markers
// into the content (those would corrupt the markdown and sync straight back).
//
// The algorithm is classic diff3, anchored on SYNC REGIONS: LCS-match the base
// against each side, intersect the two sets of matching blocks, and keep only
// the runs both sides left untouched. Those are the splice points; each gap
// between them is one change region with an exact span on all three sides. A
// region changed by only one side takes that side; changed the same way by both
// takes it once; changed differently is a conflict that takes the preferred
// side. Anchoring on agreement (rather than on one side's hunk boundaries) is
// what keeps independent edits on neighbouring lines from corrupting each other.
package merge

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// Side selects which version wins a conflict (an overlapping change both sides
// made differently). Both sides are always preserved by the caller as revisions
// regardless; this only decides what the merged text shows.
type Side int

const (
	// PreferIncoming makes the inbound file edit win a conflict hunk (the
	// just-typed local change stays visible; the DB side is recoverable).
	PreferIncoming Side = iota
	// PreferCurrent makes the server/DB version win a conflict hunk.
	PreferCurrent
)

// Conflict is one region where current and incoming both changed the same base
// lines differently. The merged output already contains the winning side; this
// records both so the caller can snapshot them.
type Conflict struct {
	Current  []string
	Incoming []string
}

// Merge3 three-way merges current and incoming against their common base,
// line-by-line. Returns the merged text and the conflicts (empty = clean).
// Splitting on "\n" makes a trailing newline a trailing empty line, so an
// unchanged side round-trips byte-for-byte.
func Merge3(base, current, incoming string, prefer Side) (string, []Conflict) {
	// Cheap, obviously-correct short-circuits.
	switch {
	case current == incoming:
		return current, nil
	case base == current:
		return incoming, nil
	case base == incoming:
		return current, nil
	}

	o := splitLines(base)
	a := splitLines(current)
	b := splitLines(incoming)

	// Walk the sync regions — runs that survived unchanged into BOTH sides — and
	// reconcile each gap between them. A gap is a change region whose extent is
	// known exactly on all three sides, which is what makes the reconcile sound.
	var out []string
	var conflicts []Conflict
	bi, ai, ii := 0, 0, 0
	for _, sr := range syncRegions(o, a, b) {
		if sr.base > bi || sr.cur > ai || sr.inc > ii {
			merged, conf := reconcile(o[bi:sr.base], a[ai:sr.cur], b[ii:sr.inc], prefer)
			out = append(out, merged...)
			if conf != nil {
				conflicts = append(conflicts, *conf)
			}
		}
		out = append(out, o[sr.base:sr.base+sr.size]...)
		bi, ai, ii = sr.base+sr.size, sr.cur+sr.size, sr.inc+sr.size
	}

	return strings.Join(out, "\n"), conflicts
}

// syncRegion is a run of lines identical in base, current AND incoming — one
// position all three agree on, and therefore the only safe place to splice.
type syncRegion struct{ base, cur, inc, size int }

// syncRegions intersects the base ranges of the (base,current) matching blocks
// with those of (base,incoming), so only a run both sides kept becomes a
// boundary. It closes with a zero-length sentinel at the three ends, so the
// final change region is bounded like any other.
//
// ⚠️ The obvious alternative — diff each side into hunks, group the hunks that
// overlap in base, then map the group's boundaries back into each side — is
// what this replaced, and it silently corrupted merges: a group's boundary is
// only stable in the side that produced it. A hunk from the other side ending
// exactly where the group begins is adjacent, not overlapping, so it never
// joined the group, and mapping through that side's alignment landed mid-hunk —
// duplicating or dropping lines with NO conflict reported. Intersecting the
// matching blocks is what makes a boundary true on both sides at once.
func syncRegions(o, a, b []string) []syncRegion {
	ma, mb := matchingBlocks(o, a), matchingBlocks(o, b)
	var out []syncRegion
	for i, j := 0, 0; i < len(ma) && j < len(mb); {
		x, y := ma[i], mb[j]
		lo, hi := max(x.bs, y.bs), min(x.bs+x.size, y.bs+y.size)
		if lo < hi {
			out = append(out, syncRegion{base: lo, cur: x.os + lo - x.bs, inc: y.os + lo - y.bs, size: hi - lo})
		}
		if x.bs+x.size < y.bs+y.size {
			i++
		} else {
			j++
		}
	}
	return append(out, syncRegion{base: len(o), cur: len(a), inc: len(b)})
}

// reconcile decides one change region. curSeg is current's lines for the region,
// incSeg incoming's, baseSeg the base's.
func reconcile(baseSeg, curSeg, incSeg []string, prefer Side) ([]string, *Conflict) {
	switch {
	case equalLines(incSeg, baseSeg):
		return curSeg, nil // only current changed here
	case equalLines(curSeg, baseSeg):
		return incSeg, nil // only incoming changed here
	case equalLines(curSeg, incSeg):
		return curSeg, nil // both made the same change
	}
	// Both sides changed this region — but the region only says they changed
	// SOMEWHERE in it. Locate each side's edit exactly; when the two touch
	// disjoint base lines they are independent and compose. That is what lets an
	// edit on one line and an edit on the next auto-merge: they are adjacent, so
	// no unchanged line separates them into regions of their own, yet neither
	// overwrites the other. Two insertions at the same point compose too, in
	// current-then-incoming order.
	if out, ok := compose(baseSeg, curSeg, incSeg); ok {
		return out, nil
	}
	conf := &Conflict{Current: cloneLines(curSeg), Incoming: cloneLines(incSeg)}
	if prefer == PreferCurrent {
		return curSeg, conf
	}
	return incSeg, conf
}

// compose splices two edits that replace disjoint stretches of the same change
// region, or reports that they overlap and must be conflicted instead.
func compose(base, cur, inc []string) ([]string, bool) {
	cLo, cHi, cRepl := locateEdit(base, cur)
	iLo, iHi, iRepl := locateEdit(base, inc)
	splice := func(aLo, aHi int, a []string, bLo, bHi int, b []string) []string {
		out := make([]string, 0, len(base)+len(a)+len(b))
		out = append(out, base[:aLo]...)
		out = append(out, a...)
		out = append(out, base[aHi:bLo]...)
		out = append(out, b...)
		return append(out, base[bHi:]...)
	}
	switch {
	case cHi <= iLo:
		return splice(cLo, cHi, cRepl, iLo, iHi, iRepl), true
	case iHi <= cLo:
		return splice(iLo, iHi, iRepl, cLo, cHi, cRepl), true
	}
	return nil, false
}

// locateEdit pins other's change against base: the half-open base range it
// replaces and the lines it puts there. A pure insertion gives an empty range at
// the insertion point.
func locateEdit(base, other []string) (lo, hi int, repl []string) {
	pre := 0
	for pre < len(base) && pre < len(other) && base[pre] == other[pre] {
		pre++
	}
	suf := 0
	for suf < len(base)-pre && suf < len(other)-pre &&
		base[len(base)-1-suf] == other[len(other)-1-suf] {
		suf++
	}
	return pre, len(base) - suf, other[pre : len(other)-suf]
}

type block struct {
	bs, os, size int // base start, other start, run length
}

// matchingBlocks groups the LCS pairs of base and other into maximal contiguous
// runs and appends a zero-length sentinel at (len(base), len(other)).
//
// The common prefix and suffix are matched OUTRIGHT, before the LCS ever runs.
// That is not only the cheap win it looks like (the O(n*m) DP then sees just the
// middle) — it is a correctness one. LCS is ambiguous over repeated lines, and
// tela's inputs are markdown: runs of blank lines and near-identical bullets.
// Merge3 intersects the (base,current) and (base,incoming) block sets, so the
// two must agree on WHERE an edit sits or a sync region is lost and the merge
// silently drops or duplicates a line. Anchoring both ends first removes most of
// that freedom and makes the two alignments agree far more often.
func matchingBlocks(base, other []string) []block {
	pre := 0
	for pre < len(base) && pre < len(other) && base[pre] == other[pre] {
		pre++
	}
	suf := 0
	for suf < len(base)-pre && suf < len(other)-pre &&
		base[len(base)-1-suf] == other[len(other)-1-suf] {
		suf++
	}

	var blocks []block
	add := func(bs, os, size int) {
		if size == 0 {
			return
		}
		if n := len(blocks); n > 0 && blocks[n-1].bs+blocks[n-1].size == bs && blocks[n-1].os+blocks[n-1].size == os {
			blocks[n-1].size += size
			return
		}
		blocks = append(blocks, block{bs: bs, os: os, size: size})
	}
	add(0, 0, pre)
	for _, p := range lcsPairs(base[pre:len(base)-suf], other[pre:len(other)-suf]) {
		add(pre+p.x, pre+p.y, 1)
	}
	add(len(base)-suf, len(other)-suf, suf)
	// Slide with the end sentinel in place, so a gap that runs to EOF is
	// canonicalized like any other; slideBlocks drops it again if it stayed empty.
	blocks = append(blocks, block{bs: len(base), os: len(other), size: 0})
	blocks = slideBlocks(base, other, blocks)
	return append(blocks, block{bs: len(base), os: len(other), size: 0})
}

// slideBlocks canonicalizes an alignment by pushing every one-sided gap as EARLY
// as it can go through the identical lines around it, then drops the runs that
// emptied out. LCS leaves a deletion inside a run of identical lines free to sit
// anywhere in that run, and Merge3 intersects two independently-computed
// alignments — so when current and incoming each delete one line from the same
// run of blank lines or repeated bullets and the two alignments park that
// deletion at different ends, the intersection reads it as two unrelated
// deletions and applies BOTH, silently losing a line neither side removed.
// Sliding both alignments to the same canonical end makes the shared edit
// coincide, so reconcile sees "both made the same change" and applies it once.
// (Same reason git's xdiff compacts its hunks.)
func slideBlocks(base, other []string, blocks []block) []block {
	for k := 0; k+1 < len(blocks); k++ {
		for blocks[k].size > 0 {
			be := blocks[k].bs + blocks[k].size
			oe := blocks[k].os + blocks[k].size
			n := &blocks[k+1]
			pureDelete := oe == n.os && be < n.bs && base[be-1] == base[n.bs-1]
			pureInsert := be == n.bs && oe < n.os && other[oe-1] == other[n.os-1]
			if !pureDelete && !pureInsert {
				break
			}
			// Hand the last matched pair of this run to the next one: the gap
			// keeps its length and its content, it just sits one line earlier.
			blocks[k].size--
			n.bs--
			n.os--
			n.size++
		}
	}
	kept := blocks[:0]
	for _, b := range blocks {
		if b.size > 0 {
			kept = append(kept, b)
		}
	}
	return kept
}

type pair struct{ x, y int }

// lcsPairs returns the matched (baseIdx, otherIdx) pairs of a longest common
// subsequence, ascending. O(n*m) DP — fine for page-sized inputs (the caller
// guards against pathological sizes before merging).
func lcsPairs(x, y []string) []pair {
	n, m := len(x), len(y)
	if n == 0 || m == 0 {
		return nil
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var pairs []pair
	i, j := 0, 0
	for i < n && j < m {
		if x[i] == y[j] {
			pairs = append(pairs, pair{i, j})
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			i++
		} else {
			j++
		}
	}
	return pairs
}

func splitLines(s string) []string { return strings.Split(s, "\n") }

func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cloneLines(a []string) []string {
	if len(a) == 0 {
		return nil
	}
	out := make([]string, len(a))
	copy(out, a)
	return out
}

// MergeProps three-way merges the props bag field-by-field (sync spec §5: props
// are structured-merged, not text-diffed). A key changed by only one side takes
// that side (including deletion); both sides changing it the same way is no
// conflict; a divergent change takes prefer and is reported. Returns the merged
// bag and the conflicting keys.
func MergeProps(base, current, incoming map[string]any, prefer Side) (map[string]any, []string) {
	out := map[string]any{}
	var conflicts []string
	for _, k := range unionKeys(base, current, incoming) {
		bv, bok := base[k]
		cv, cok := current[k]
		iv, iok := incoming[k]
		curChanged := !sameVal(bv, bok, cv, cok)
		incChanged := !sameVal(bv, bok, iv, iok)
		switch {
		case !incChanged:
			if cok {
				out[k] = cv
			}
		case !curChanged:
			if iok {
				out[k] = iv
			}
		case sameVal(cv, cok, iv, iok):
			if cok {
				out[k] = cv
			}
		default:
			conflicts = append(conflicts, k)
			if prefer == PreferCurrent {
				if cok {
					out[k] = cv
				}
			} else if iok {
				out[k] = iv
			}
		}
	}
	return out, conflicts
}

// Scalar three-way merges a single value (the page title). conflicted is true
// when both sides changed it to different values.
func Scalar(base, current, incoming string, prefer Side) (merged string, conflicted bool) {
	switch {
	case current == incoming:
		return current, false
	case current == base:
		return incoming, false
	case incoming == base:
		return current, false
	default:
		if prefer == PreferCurrent {
			return current, true
		}
		return incoming, true
	}
}

func unionKeys(maps ...map[string]any) []string {
	seen := map[string]bool{}
	var keys []string
	for _, m := range maps {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// sameVal reports whether two (value, present) props entries are equal, by
// JSON-canonical comparison so a YAML int and a JSONB float of the same value
// match (mirrors the api-layer propsEqual). Absent on both = equal.
func sameVal(a any, aok bool, b any, bok bool) bool {
	if aok != bok {
		return false
	}
	if !aok {
		return true
	}
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return bytes.Equal(ja, jb)
}
