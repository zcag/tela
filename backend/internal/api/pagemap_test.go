package api

import "strings"

import "testing"

const samplePage = `Intro line.

## Setup

Install the thing.

### Linux

Use apt.

## Deploy

Run make deploy.

` + "```" + `bash
## not a heading (inside a fence)
` + "```" + `

## Notes

Old notes here.
`

func TestPageOutline(t *testing.T) {
	secs := pageOutline(samplePage)
	got := make([]string, len(secs))
	for i, s := range secs {
		got[i] = s.Path
	}
	want := []string{"Setup", "Setup > Linux", "Deploy", "Notes"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("outline paths = %v, want %v", got, want)
	}
	// Preview must be the section's OWN content (Setup → "Install the thing.",
	// not its Linux subsection), and a fenced "## not a heading" must not leak in.
	if secs[0].Preview != "Install the thing." {
		t.Fatalf("Setup preview = %q, want %q", secs[0].Preview, "Install the thing.")
	}
	if strings.Contains(secs[2].Preview, "not a heading") {
		t.Fatalf("Deploy preview leaked fenced text: %q", secs[2].Preview)
	}
}

func TestApplyPatch(t *testing.T) {
	cases := []struct {
		name, target, op, content string
		wantContains, wantAbsent  string
	}{
		{"append", "Deploy", "append", "Then verify.", "Then verify.", ""},
		{"prepend", "Setup", "prepend", "Prereqs first.", "Prereqs first.", ""},
		{"replace", "Setup > Linux", "replace", "Use the new installer.", "Use the new installer.", "Use apt."},
		{"delete", "Notes", "delete", "", "", "Old notes here."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := applyPatch(samplePage, c.target, c.op, c.content)
			if err != nil {
				t.Fatalf("applyPatch: %v", err)
			}
			if res.Section == nil {
				t.Fatalf("no section matched")
			}
			if c.wantContains != "" && !strings.Contains(res.Body, c.wantContains) {
				t.Fatalf("output missing %q:\n%s", c.wantContains, res.Body)
			}
			if c.wantAbsent != "" && strings.Contains(res.Body, c.wantAbsent) {
				t.Fatalf("output should have dropped %q:\n%s", c.wantAbsent, res.Body)
			}
			// The fence content must survive every patch (never treated as a heading).
			if !strings.Contains(res.Body, "## not a heading") {
				t.Fatalf("fenced text was lost / misparsed:\n%s", res.Body)
			}
		})
	}
}

// Replacing a section takes its sub-sections with it — the standard span
// definition, but destructive in a way append/prepend are not. The loss has to
// come back in the result, or a caller can't notice it happened.
func TestApplyPatchReportsRemovedSubsections(t *testing.T) {
	res, err := applyPatch(samplePage, "Setup", "replace", "New setup steps.")
	if err != nil {
		t.Fatalf("applyPatch: %v", err)
	}
	if strings.Contains(res.Body, "### Linux") {
		t.Fatalf("replace should span the subtree:\n%s", res.Body)
	}
	if len(res.Removed) != 1 || res.Removed[0] != "Setup > Linux" {
		t.Fatalf("removed = %v, want [Setup > Linux]", res.Removed)
	}
	// A leaf section removes nothing, so a caller isn't warned about non-losses.
	res, err = applyPatch(samplePage, "Setup > Linux", "replace", "Use the new installer.")
	if err != nil {
		t.Fatalf("applyPatch leaf: %v", err)
	}
	if len(res.Removed) != 0 {
		t.Fatalf("leaf replace reported removals: %v", res.Removed)
	}
}

// The "do this now" callout is both the most-edited block on a page and, until
// the outline learned about blockquotes, the only one that needed a whole-page
// rewrite to touch.
const calloutPage = `## Where we are

Some prose.

> [!IMPORTANT]
> ### One thing to do right now
>
> Read the brief.

After the callout.

## Next
`

func TestCalloutHeadingIsPatchable(t *testing.T) {
	secs := pageOutline(calloutPage)
	var quoted *pageSection
	for i := range secs {
		if secs[i].Heading == "One thing to do right now" {
			quoted = &secs[i]
		}
	}
	if quoted == nil {
		t.Fatalf("callout heading not in outline: %v", sectionPaths(secs))
	}
	if !quoted.InQuote {
		t.Fatal("callout heading not marked in_quote")
	}

	res, err := applyPatch(calloutPage, "One thing to do right now", "replace", "Read the OTHER brief.")
	if err != nil {
		t.Fatalf("applyPatch: %v", err)
	}
	// The new content must stay INSIDE the blockquote, and the section must stop
	// at the end of the quote rather than swallowing the prose after it.
	if !strings.Contains(res.Body, "> Read the OTHER brief.") {
		t.Fatalf("patched content broke out of the callout:\n%s", res.Body)
	}
	if strings.Contains(res.Body, "Read the brief.") {
		t.Fatalf("old callout body survived:\n%s", res.Body)
	}
	for _, keep := range []string{"> [!IMPORTANT]", "After the callout.", "## Next"} {
		if !strings.Contains(res.Body, keep) {
			t.Fatalf("patch ate %q:\n%s", keep, res.Body)
		}
	}
}

func TestApplyPatchUnknownTarget(t *testing.T) {
	if _, err := applyPatch(samplePage, "Nonexistent", "append", "x"); err == nil {
		t.Fatal("expected error for unknown target")
	}
}
