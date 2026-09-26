// Package reviewdecision computes the Referral `lydite review` reaches for a
// change, from everything it is decided over: the change's own diff read
// against a resolved base, the exemptions that base declares, a breaking
// change the pull request's title or a commit declares, the public-API
// comparison every opted-in component asked for, and the dependency delta of
// every manifest the change touches.
//
// It is named for that decision rather than folded into internal/referral
// because internal/referral only matches a change against an exemptions file,
// and is handed every other verdict already decided (see referral.Evidence).
// Gathering that evidence means resolving revisions, reading files out of
// commits, materialising the base as a worktree and running each language's
// comparison — none of which belongs behind a matcher whose value is that it
// has none of them. review, clearance and clearance queue each recompute the
// decision here, so the fingerprint a clearance records and the one a
// merge-queue entry is compared under come out of the same code.
//
// It returns data and writes nothing of its own: a warning the computation
// raises is returned for the caller to write, and rendering a row from the
// result is the caller's.
package reviewdecision

import (
	"context"
	"fmt"
	"path"
	"strings"

	"lydite/lydite/internal/declaration"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/referral"
)

// Input is what a decision is computed over.
type Input struct {
	// Dir is the scan root, which locates the exemptions file and the
	// component declaration. The diff itself covers the whole repository.
	Dir string
	// Base is the commit the change is measured against, as ResolveBase
	// returned it.
	Base string
	// Surfaces is each opted-in component's public-API comparison, as
	// Surfaces or CompareSurfaces returned it.
	Surfaces []SurfaceComparison
	// Title resolves the pull request's title, and is nil where there is no
	// pull request. It is called once, after the exemptions and the diff have
	// been read, so a caller whose resolution costs a request to the platform
	// or warns about a failure pays for neither on a run that stopped before
	// the declaration was ever read.
	Title func() string
	// ScanEvidence reports whether the licence gate and the advisory check ran
	// and passed for every component, and is nil where no scan document was
	// given. It is called at most once, and only when every version the change
	// moves moved by a patch or a minor: the condition is met only when both
	// halves hold, and the second is not asked for when the first already
	// failed.
	ScanEvidence func() bool
}

// Result is the decision, and what it was computed from that a caller renders.
type Result struct {
	// Decision is the referral, with every disqualification the API-surface
	// comparison, the break declaration and the dependency delta add already
	// folded in.
	Decision referral.Decision
	// File is the exemptions file read out of the base commit.
	File referral.File
	// BreakDeclared names where the change declares a breaking API change, and
	// is empty when nothing does.
	BreakDeclared string
	// Dependencies is what the change did to every dependency manifest it
	// touches, in the order the diff names them.
	Dependencies []ManifestDelta
	// Warnings are the lines a caller writes to its diagnostics stream, in the
	// order they arose. Each is a whole line without its newline. Every one
	// arises after Title and ScanEvidence were called, so writing them once
	// Decide returns keeps them after anything those two wrote.
	Warnings []string
}

// Decide computes the decision review reaches: the exemptions at the base,
// the change's own diff, and the evidence each check adds to it.
//
// The dependency comparison is measured before referral.Decide runs, because a
// conditional exemption is tested against how far the versions moved; the
// disqualifications that same comparison carries are added after it, with the
// API-surface ones, so the order a reader sees them in is the diff's own, then
// the declaration's, then each component's, then each manifest's.
func Decide(ctx context.Context, in Input) (Result, error) {
	file, change, err := read(ctx, in.Dir, in.Base)
	if err != nil {
		return Result{}, err
	}

	deltas := measureDependencies(ctx, in.Dir, in.Base, change.Paths)
	evidence := referral.Evidence{
		PatchAndMinor: versionsPatchAndMinor(deltas) && in.ScanEvidence != nil && in.ScanEvidence(),
	}
	decision := referral.Decide(change, file, evidence)

	var title string
	if in.Title != nil {
		title = in.Title()
	}
	where, warning := breakDeclaration(ctx, in.Dir, in.Base, title)
	var warnings []string
	if warning != "" {
		warnings = append(warnings, warning)
	}
	referSurfaces(&decision, where, in.Surfaces)
	referDependencies(&decision, deltas)

	return Result{
		Decision:      decision,
		File:          file,
		BreakDeclared: where,
		Dependencies:  deltas,
		Warnings:      warnings,
	}, nil
}

// DecideFromDiff computes the decision from the diff and the base's exemptions
// alone: no API-surface comparison, no dependency delta, no declaration, and
// the zero referral.Evidence, under which every condition fails.
//
// This is the decision a merge-queue entry is recomputed under. The queue's
// job has none of the toolchain provisioning or worktree machinery a
// comparison needs, and no scan document a `versions:` condition could be met
// from; passing nothing is the direction that refers, so an entry whose
// clearance was given against a condition, a declared break or an added
// dependency fingerprints differently here and goes back to a person rather
// than being carried forward on evidence nobody measured.
func DecideFromDiff(ctx context.Context, dir, base string) (referral.Decision, error) {
	file, change, err := read(ctx, dir, base)
	if err != nil {
		return referral.Decision{}, err
	}
	return referral.Decide(change, file, referral.Evidence{}), nil
}

// read is the exemptions file at the base, then the change against it.
func read(ctx context.Context, dir, base string) (referral.File, referral.Change, error) {
	file, err := exemptionsAt(ctx, dir, base)
	if err != nil {
		return referral.File{}, referral.Change{}, err
	}
	change, err := referral.Changes(ctx, dir, base)
	if err != nil {
		return referral.File{}, referral.Change{}, err
	}
	return file, change, nil
}

// referSurfaces folds the declaration and each component's comparison into the
// decision.
//
// Three verdicts, and which one a break gets is decided by the declaration
// alone (see docs/adr/0040):
//
//   - an undeclared break is a gate the caller renders as failing, and adds no
//     disqualification: the author clears it by not breaking the API or by
//     declaring the break, and both are work they can do;
//   - a declared break refers, because every breaking change should reach a
//     person;
//   - a surface that could not be compared refers, because review genuinely
//     cannot tell a break from no break and neither pass nor fail is true.
//
// The declaration is honoured on its own: a change that declares a break is
// referred even where no component opted in and nothing was compared. That is
// the one-way ratchet the marker is only safe under — the claim can add a
// referral and can never remove one, so spelling `feat!:` rather than `feat:`
// never makes anything greener.
func referSurfaces(d *referral.Decision, where string, results []SurfaceComparison) {
	if where != "" {
		refer(d, referral.Disqualification{
			Kind:     referral.DisqualificationAPIBreakDeclared,
			Evidence: "declared in " + where,
		})
	}
	for _, res := range results {
		if res.Uncomputable == "" {
			continue
		}
		refer(d, referral.Disqualification{
			Kind:     referral.DisqualificationAPISurfaceUncomputable,
			Path:     res.Dir,
			Evidence: res.Component + ": " + withNote(res.Uncomputable, SkippedNote(res.Skipped)),
		})
	}
}

// refer adds a disqualification and states the verdict that follows from it.
//
// Both, together, always. A disqualification appended without Referred set
// would be rendered and then have no effect at all on a change an exemption
// covered — which is the one case where adding it mattered.
func refer(d *referral.Decision, dq referral.Disqualification) {
	d.Disqualifications = append(d.Disqualifications, dq)
	d.Referred = true
}

// breakDeclaration names where this change declares a breaking API change, or
// returns empty when nothing does, with the warning a reader needs when the
// commits could not be read.
//
// Both sources are read, because the two answer different questions and
// neither subsumes the other. Squash merge makes the title the commit that
// lands, so a break declared only in a commit about to be squashed away
// leaves no marker in the history; locally there is no pull request and no
// title, and the commits are all there is.
func breakDeclaration(ctx context.Context, dir, base, title string) (string, string) {
	if declaration.Declared(title) {
		return "the pull request title", ""
	}
	messages, err := gitstate.CommitMessages(ctx, dir, base, "HEAD")
	if err != nil {
		// Said out loud rather than swallowed: unread commits can only ever
		// under-refer, and a reader who declared a break in one of them would
		// otherwise see no sign that the declaration was never looked at.
		return "", fmt.Sprintf("warning: could not read the commits in %s..HEAD (%v) — a break declared only in one of them is not seen", short(base), err)
	}
	for _, message := range messages {
		if declaration.Declared(message) {
			return "a commit in " + short(base) + "..HEAD", ""
		}
	}
	return "", ""
}

// ResolveBase turns a requested base into a full commit SHA, and refuses
// anything that is not one.
//
// "auto" reuses the merge-base internal/gitstate already resolves for the
// coverage gate, so a change's scan, coverage and referral all agree on what
// "this change" means. An unresolvable "auto" is an error rather than a
// silent fallback: guessing a base would silently change which paths the
// verdict was computed from, and a shallow checkout is a fixable
// misconfiguration.
//
// Every base is then resolved through `git rev-parse --verify` and required
// to be an ancestor of HEAD, and the resolved SHA — never the caller's
// string — is what reaches git afterwards. Three separate failures ride on
// this, and each one turns the gate into a rubber stamp rather than a
// blocker:
//
//   - An empty base makes the range read "..HEAD", which git resolves to an
//     empty diff. No changed paths means nothing to cover and no line to
//     scan, so the run passes with every disqualifier silent.
//   - An empty base also makes the exemptions spec read ":<path>", which git
//     resolves against the *index* — handing the branch control of the
//     allowlist the merge-base read exists to deny it.
//   - A base beginning with "-" lands in an argv position git reads as an
//     option, so "--base=--output=/tmp/x" is an arbitrary file write.
//
// A base that is not an ancestor of HEAD is refused for the same reason:
// the diff would describe something other than what this branch introduces.
func ResolveBase(ctx context.Context, dir, base, baseBranch string) (string, error) {
	if base == "" {
		return "", fmt.Errorf("--base is empty: name a commit, or use \"auto\" to resolve the merge-base with the base branch")
	}
	if base == "auto" {
		baseSHA, err := gitstate.ResolveBaseSHA(ctx, dir, baseBranch)
		if err != nil {
			return "", fmt.Errorf("--base auto: %w (a full-history checkout is required — set fetch-depth: 0)", err)
		}
		base = baseSHA
	}
	// --end-of-options stops a value beginning with "-" from being read as
	// an option, which is what makes verifying it here sufficient to protect
	// every later invocation.
	rev := executil.RunQuiet(ctx, dir, "git", "rev-parse", "--verify", "--quiet", "--end-of-options", base+"^{commit}")
	resolved := strings.TrimSpace(rev.Output)
	if !rev.Ok() || resolved == "" {
		return "", fmt.Errorf("--base %q does not name a commit", base)
	}
	if r := executil.RunQuiet(ctx, dir, "git", "merge-base", "--is-ancestor", resolved, "HEAD"); !r.Ok() {
		return "", fmt.Errorf("--base %s is not an ancestor of HEAD, so the diff would not describe this branch", resolved[:12])
	}
	return resolved, nil
}

// exemptionsAt reads the exemptions file out of the base commit, never out of
// the working tree.
//
// A change that widens the gate must get no benefit from its own widening.
// Reading the file from the branch would let one pull request declare itself
// exempt, which is the entire attack this ordering exists to remove.
//
// An absent file is the day-one state and not an error: it declares no
// exemptions, so everything is referred. A file that exists and cannot be
// read is a different thing entirely, and showAtRevision keeps the two apart.
func exemptionsAt(ctx context.Context, dir, base string) (referral.File, error) {
	prefix, err := referral.RootRelative(ctx, dir)
	if err != nil {
		return referral.File{}, err
	}
	repoPath := path.Join(prefix, referral.FileName)
	content, present, err := showAtRevision(ctx, dir, base, repoPath)
	if err != nil {
		return referral.File{}, err
	}
	if !present {
		return referral.File{}, nil
	}
	return referral.Parse(content, repoPath+" at "+short(base))
}

// showAtRevision reads a repository-root-relative path out of a commit,
// never out of the working tree, and says separately whether the commit has
// the path at all.
//
// The two questions are asked with two commands — `cat-file -e` answers "is
// it there", and only then does `show` read it. Collapsing them would make a
// broken read indistinguishable from a path the commit never had, and every
// caller here treats those differently: an absent exemptions file declares no
// exemptions, an absent manifest states no dependencies, and a read that
// failed states nothing at all.
func showAtRevision(ctx context.Context, dir, rev, repoPath string) ([]byte, bool, error) {
	spec := rev + ":" + repoPath
	if r := executil.RunQuiet(ctx, dir, "git", "cat-file", "-e", spec); !r.Ok() {
		return nil, false, nil
	}
	r := executil.RunQuiet(ctx, dir, "git", "show", spec)
	if !r.Ok() {
		return nil, true, fmt.Errorf("reading %s at %s: %w: %s",
			repoPath, short(rev), r.Err, strings.TrimSpace(r.Stderr))
	}
	return []byte(r.Output), true, nil
}

// ListCap bounds every enumeration a decision's evidence carries, and every
// one the report rendering it carries.
//
// A referral on a large change can name hundreds of files, and a verdict a
// reader has to scroll past hundreds of lines to reach is one they stop
// reading. The cap is not an abbreviation of the finding — the finding is
// "this change is not exempt", which one example establishes as well as
// three hundred — it is an abbreviation of the evidence.
const ListCap = 8

// Capped truncates a list to ListCap, replacing the tail with a count so the
// evidence never implies it showed everything.
func Capped(items []string) []string {
	if len(items) <= ListCap {
		return items
	}
	return append(items[:ListCap:ListCap], fmt.Sprintf("…and %d more", len(items)-ListCap))
}

// short is a commit as a reader is shown it.
func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
