# lydite
lydite unifies SAST/SCA/lint/coverage gating for Rust, TypeScript, and Go projects into one CLI, run identically locally and in CI.

## Language

**Aggregate coverage**:
The current whole-tree coverage percentage for one ecosystem, compared against a cached per-main-commit baseline (`lydite` branch). Catches regressions in code the current PR never touches (e.g. a deleted test file).
_Avoid_: "total coverage", "overall coverage".

**Patch coverage**:
The coverage percentage of only the lines added/modified by the current PR (HEAD vs. merge-base), gated against that same ecosystem's aggregate baseline (`patch% >= baseline%`). Catches untested new code even when the codebase is too large for it to move the aggregate percentage. Computed alongside aggregate coverage, not instead of it — they catch disjoint regression classes.
_Avoid_: "diff coverage" (used interchangeably by other tools like Codecov, but this repo standardizes on "patch coverage").

**Baseline**:
The aggregate coverage value cached on the `lydite` branch for a specific main-branch commit SHA, computed once per SHA. Both aggregate and patch coverage compare against this same value — patch coverage has no baseline concept of its own.

**Coverable line**:
A source line that a language's own coverage tool (`go tool cover`, `cargo llvm-cov`, Istanbul) reports an entry for. Comments, blank lines, imports, and braces are never coverable — they simply never appear in a coverage report, so patch coverage's denominator (coverable changed lines) excludes them automatically, without lydite doing any language-aware filtering itself.

**Linter**:
The engine backing lydite's TypeScript check: Biome, and only Biome. `typescript.linter` in `.lydite/config.yml` accepts `biome` alone; the retired `eslint` value is rejected with an error rather than accepted and quietly run under Biome.
_Note_: the TypeScript check gates on **correctness** as well as security, so it is not "security findings only" the way the other language checks are. The ESLint stack it replaced covered a different set of security rules — Node/backend heuristics with no Biome equivalent — so this is a change in what is gated, not only in what runs it. See [ADR 0008](docs/adr/0008-biome-as-the-only-typescript-linter.md).
_Avoid_: "linting mode", "the TS linter setting", describing Biome as opt-in.

**Pin**:
The exact version of a tool lydite installs and runs, recorded in a real package-manager manifest (`package.json`, `Cargo.toml`, `go.mod`, `requirements.txt`) so Dependabot can see and bump it — never only in a Go constant. The distinguishing property of a pin is that something must be able to *age it out*: a pin nothing can bump is indistinguishable from a scanner that has silently stopped being current.
_Avoid_: "vendored version", "bundled version" (lydite vendors no tool source; it installs pinned releases).

**Quality history**:
The record of a project's quality measurements over time, accumulated across runs and rendered as a dashboard. Distinct from **Baseline**, which is a single cached value for one commit: a baseline answers "did this PR regress?", quality history answers "where has this project been trending?".
_Avoid_: "reporting" (already means uploading to Codecov/Semgrep in `action.yml`, and `coverage.source: report` means a pre-existing coverage file), "state" (conflates it with the cache).

**Ledger**:
A measurement record that cannot be recomputed after the fact — the toolchain **Pin**s that produced it may have moved, and the commit may no longer exist after a squash-merge. Ledger entries are append-only and are never deleted, only compacted. Contrast **Cache**.

**Cache**:
Derived data that can be regenerated on demand by re-running the tool that produced it. Losing a cache entry costs time, not information, so cache writes are best-effort and non-fatal. The per-commit **Baseline** is a cache; **Quality history** is a **Ledger**. The distinction is not stylistic: it dictates whether a failed write may be ignored.

**Projection**:
A pre-computed rollup derived from the **Ledger**, existing so the dashboard reads one small file instead of walking every partition. Regenerable by definition, so it is a **Cache** in every respect except that its source is the ledger rather than a scanner.

**Finding snapshot**:
The full set of findings from the most recent run — rule, path, severity, message — overwritten on every run. A **Cache**, not a **Ledger**: it is fully regenerable by re-scanning, and only the latest one is ever of interest. It answers "what is wrong right now, and where?"; the ledger answers "is this getting better?". Keeping findings out of the ledger is what lets quality history stay small enough to retain forever.
_Note_: because snapshots are not retained, "when did this finding first appear?" is deliberately unanswerable today. Answering it later means adding stable per-finding fingerprints and promoting findings to a ledger of their own — an additive change, which is why the split is the cheaper starting point.

**Gap**:
An interval in the **Quality history** where a run happened but its ledger append did not land (a push race, a token scope, a branch-protection rule). Because ledger writes are non-fatal, gaps are possible by design — so they are recorded explicitly and rendered as a break in the trend line. The governing invariant is not "there are no gaps" but *the ledger never lies about its own completeness*: a chart that shows where data is missing is trustworthy, one that interpolates across a hole is not.

**Component**:
The unit lydite schedules, builds, tests and gates: one language rooted at one directory, declared in `.lydite/components.yml`. Components are what run in parallel — as CI matrix jobs, or as local processes — and a component carries its runner, the services that runner needs, and its per-component opt-outs.
A component is **the unit that language's own build tool treats as a whole**: a Cargo workspace, a Go module, a JavaScript workspace. It is not a deployable, and the two come apart routinely — eleven crates behind one `cargo --workspace` invocation are one component, because splitting them means compiling eleven times and provisioning eleven copies of everything the suite needs. Splitting below the build unit costs far more than the parallelism it buys.
_Note_: components are declared rather than derived, so the declaration is reviewable and its history records every change to what gets tested. A source file under no component's directory and under no declared exclude is an **Orphan**, and reporting one is a **Gate** — the author clears it by declaring a component or excluding the path. Without that check a declared list fails open: nothing breaks when it goes stale, so new code is simply never tested and the build stays green.
_Avoid_: "package", "module", "project", "workspace" — each already names something specific in one language's tooling, and a component is the thing that spans all three. Avoid "service" for a component: a component may contain several services, and `services` names what its tests depend on.

**Orphan**:
A source file under no **Component**'s directory and under no declared exclude. Orphans are what keep a declared component list from failing open: without the check, a directory nobody declared is tested by nobody while every run reports green. Reporting one is a **Gate** — the author clears it by declaring a component or by writing the exclude, and the exclude is the reviewable statement that this code is tested by nobody and somebody decided that.
_Note_: it is a question about paths alone, deliberately. It reads no manifest and parses no source, which is what lets it catch a whole undeclared directory that has no manifest in it yet — the case detection cannot see at all. A file counts only if it is written in a language lydite has a runner for: a `README.md` or a shell script is not code any component could claim, so requiring an exclude for one would be paperwork for a question lydite cannot act on.
_Avoid_: "untracked" (git's word, and an orphan is usually tracked), "uncovered" (that is coverage's), "unowned" — ownership is a people question and this is a testing one.

**Proving ground**:
A repository that exists only to be run against, holding the shapes lydite must handle rather than code anyone deploys. It is the ground truth for behaviour that is not a function and so cannot be unit-tested — a CI matrix fanning out, affected selection over a real diff, two components contending for a port — and for the languages lydite's own repository cannot exercise. Its correct verdict is not all-green: a gate that never fires against it is a gate nothing has observed.
_Note_: it is deliberately awkward. Every shape in it is one that changed a design decision, so tidying one removes the evidence for that decision. What keeps it honest is being run on a schedule by the tool it validates — a proving ground nothing exercises drifts silently, and a stale one is worse than none because it still reports success.
_Avoid_: "fixture" and "test data" for the repository as a whole — both suggest something living inside the tree under test, and the point is that lydite runs against it as a foreign repository. Avoid "example" and "demo": nothing here is meant to be copied or shown.

**Gate**:
A check a change must satisfy to merge, which the change's author clears by doing more work — adding an assertion that kills a surviving mutant, raising patch coverage. Gates are meant to be iterated against, including by an agent, because every way of satisfying one improves the code. Contrast **Referral**.
_Avoid_: using "gate" for the whole of lydite's verdict — a referral is not a gate.

**Referral**:
lydite's decision that a change needs human review before it merges. A referral names no defect and is not an accusation: most referrals mean only that no **Exemption** matched. It makes no demand, so unlike a **Gate** there is no work that satisfies it — the author can still change the change until it matches a declared shape, but nothing they can do to *this* change clears it. It is also the only lydite verdict that is pending rather than terminal: it is resolved by a **Clearance**, which lives outside the repository.
_Avoid_: "escalation", "block" (a referral asserts nothing is wrong), "warning" (a referral is not advisory — nothing merges past it), "failure" (a **Gate** fails; a referral does not).

**Exemption**:
A declared shape of change that merges without a **Referral** — a README-only edit, a dependency bump with a clean SCA run. Exemptions are an allowlist: a change matching none of them is referred, so the set states positively what may merge unattended rather than trying to enumerate what may not. The set is therefore the whole risk model; there is no separate score or threshold.
_Avoid_: "rule", "ignore", and especially "suppression" — a suppression is a finding-level annotation in the scanned code, and an *input* to referral rather than a way around one.
_Note_: a change that alters the exemption set may alter nothing else. That isolation is a **Gate**, not a **Referral** — the author clears it by splitting the change — and it exists so the exemption set's history is the complete record of what may merge unread, rather than something a reviewer has to notice inside a larger change.

**Disqualifier**:
Evidence in a change that vetoes any **Exemption** match, so the change is referred however boring its shape. Disqualifiers are not measurements of the code: they are signs that something tried to make a verdict go away — a net-new suppression annotation, a newly skipped test, an edit to lydite's own configuration or to CI. They live in their own list rather than as conditions inside each exemption, because otherwise every future exemption would have to remember every disqualifier and forgetting one would be silent. The built-in set cannot be switched off; a repo's own list only adds to it.
_Note_: a disqualifier must be readable off the diff. Anything the author merely *asserts* about their own change — a commit-message marker, an "equivalent mutant" annotation — may add a referral but may never remove one, since an agent can simply decline to assert it. See [ADR 0014](docs/adr/0014-evidence-only-referral-matching.md).
_Avoid_: "veto rule", "blocker", and using it for a **Gate** failure — a failing gate is the author's to clear, a disqualifier is not.

**Clearance**:
A human's resolution of a **Referral**, bound to the exact revision it was given for. The revision is one commit, not the tree it produces and not the shape of the verdict: a clearance carries no notion of two revisions being "the same change", so any further change to the branch — a rebase that alters nothing, included — voids it. A clearance is never standing permission for a pull request.
The human is someone with write access to the repository, established from the hosting platform rather than from anything the commenter says about themselves. That is a floor and not the whole of the trust: whoever holds the repository's credentials can give a clearance, which is what makes it conventional today.
_Avoid_: "approval" — GitHub's review approval is a different mechanism, with different rules about who may give one.

## Flagged ambiguities
**"Threshold"** for referral — there is none, deliberately. Because **Exemption**s are an allowlist and the default is to refer, lydite computes no riskiness score and has nothing to tune. `coverage.tolerance` and `coverage.patch.tolerance` are **Gate** knobs and say nothing about whether a change is referred.

**"Threshold"** — the original patch-coverage feature request proposed a `coverage.patch.threshold` config (an arbitrary fixed percentage per language). This was superseded: patch coverage has no independent threshold — it always gates against the aggregate baseline (see **Patch coverage**). Only an `enabled: bool` (default `true`, opt-out) remains in config.

## Example dialogue
> **Dev:** If a PR adds 9 new lines with 0% patch coverage, does lydite fail it?
> **Domain expert:** Only if the aggregate baseline for that language is above 0% — patch coverage gates against baseline, not a fixed number. If the language has no baseline yet (first time seen), it's reported informationally and doesn't fail, same as aggregate coverage's `[NEW]` case.
> **Dev:** What about a changed comment line — does that count against patch coverage?
> **Domain expert:** No — it's never a coverable line, so it's excluded automatically once we intersect changed lines with what the coverage tool actually reports.
