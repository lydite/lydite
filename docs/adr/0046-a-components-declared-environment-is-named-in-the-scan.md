# A component's declared environment is named in the scan, and nothing about it is refused

[ADR 0020](0020-scan-on-components.md) composes a component's declared `env:` into the checks
that scan it, and closes the part of that channel which reaches lydite's *own* binaries: a
declared `PATH` goes behind the inherited one, the resolved toolchain's variables compose last,
and installs run with lydite's toolchain rather than the repository's. It then names what it
leaves open, deliberately:

> A declared `GOFLAGS` still changes which files gosec compiles, and a declared `GOVULNDB` still
> points govulncheck at another database […] closing that means an allowlist of what a
> declaration may say, which is a list that must be complete to be safe — the property ADR 0018
> refuses for the invalidator set.

[#56](https://github.com/lydite/lydite/issues/56) is the open half. It is not closed by a filter,
because the filter is the allowlist. It is closed the way the `.semgrepignore` half of the same
paragraph is closed: **by naming it.** `lydite scan` reports, on stderr, the environment variable
names a component's checks ran with, per component, before the checks run. The warning never
gates, nothing is refused, and the declaration stays the repository's own.

## Names, never values

**The warning prints variable names. It never prints a value, and no future refinement of it
prints a value.** This is a rule of the feature and not a detail of how it happens to be
implemented.

A declared value is arbitrary text the repository controls, and the places a repository puts a
token are exactly the places that look like configuration: a registry URL with credentials in it,
a `*_TOKEN` a suite needs, a DSN. This text reaches a CI log, which on a public repository is
world-readable, and the report it sits beside reaches a pull-request comment. lydite already
treats an environment value as potentially secret where it runs untrusted code —
`apisurface.credentialFree` drops every key matching `TOKEN`, `SECRET`, `KEY`, `PASSWORD`,
`CREDENTIAL` before `packages.Load` sees it, and is deliberately broad because a variable let
through by mistake is worse than one held back. Printing values would be that judgement reversed.

Redacting values rather than omitting them was considered and rejected for the same reason the
allowlist is rejected: a redactor is a pattern list that must be complete to be safe, and an
incomplete one publishes the secret it did not recognise while reading as though it had checked.
A name is enough for the whole purpose here — what a reader needs to know is *that* `GOVULNDB`
was set for this component's govulncheck, and the value is in
`.lydite/components.yml`, in the repository, under review.

The one thing names-only gives up is that an empty value reads the same as a full one. That is
accepted: `env: {GOFLAGS: ""}` and `env: {GOFLAGS: "-tags ignore_me"}` both mean this
component's checks compiled under a `GOFLAGS` the declaration chose, and the name is what carries
that.

## A stderr warning, not a row

The report is stdout and warnings are stderr
([`output-grammar.md`](../../agentic/references/output-grammar.md)), and this is a warning:
a statement about the conditions a check ran under, not a claim about the code. It is the shape
`warnSemgrepignore` already has — stat, one line, before the tool runs — and having two
statements of the same kind share one shape is worth more than either of them individually.

A row was the alternative, and it loses on a question it cannot answer: what status. A row votes
or it abstains. A `pass` row for a component that declared `GOVULNDB` asserts something nobody
measured. A `fail` or `refer` row makes a declaration the repository is entitled to make into a
gate, which is the refusal this ADR rules out below. An `unmeasured` row is the honest one and is
the worst of the three in practice — `unmeasured` exists for a gate that could not run, and
spending it on a component that scanned perfectly well trains readers to skip the tag that exists
to be noticed, the same argument ADR 0020 makes against a row per opted-out component.

**It is not in `--json`, and this is the one place the shape of the recommendation is not
followed.** The `--json` document is rows, its keys are a pinned contract
(`TestJSONKeysArePartOfTheContract`), and adding a top-level field to it buys a consumer that
does not exist: `lydite/actions` reads documents to decide what to publish, and decision four
below says the surface does not carry this. Meanwhile stderr is unaffected by `--json` — under
`--json` the document goes to stdout and everything else, scanner output included, still goes to
stderr — so a `--json` run loses nothing. The warning is in the CI log of every run either way.

## Every declared name, with the ones lydite knows about marked

The warning names **every** variable the component declared, always.

Naming only the ones lydite recognises as steering a check would be the allowlist inverted, and
inherits its defect exactly: the set has to be complete to be trusted, a variable missing from it
reads as a component that declared nothing, and the day a scanner gains a new environment
variable the omission is silent. Uniform reporting has no such failure — a name lydite has never
heard of is printed like any other.

On top of that, a name lydite **does** know steers a check is marked as such, from a short list
carried in the code: `GOFLAGS`, `GOVULNDB`, `GOPRIVATE`, `RUSTFLAGS`, `RUSTC_WRAPPER`,
`CARGO_BUILD_TARGET`, `NODE_OPTIONS`, and the rest of the same kind. **That list is best-effort
and is explicitly not a security boundary.** It may rot, and rotting is harmless: a variable that
falls off it is still printed, just without the mark. Nothing reads the marked set to decide
anything, so there is no behaviour to go wrong when it is wrong — which is the property that
makes it safe to have at all, and the property an allowlist can never have.

The mark earns its place because the uniform list is long in a real repository, and a reader
scanning a CI log for the thing worth looking at gets one. A hint that is allowed to be
incomplete is a different object from a filter that is not.

## Beside the scan's own per-component loop, over what was composed

The warning is emitted in `cmd/lydite/scan.go`'s per-component loop, where `executil.Env` is
built, and it reports **what was composed into the child environment** rather than what the YAML
said. The two differ in ways that matter to a reader:

- A declared `PATH` is not a variable of the child at all — `splitPath` folds it into the single
  composed PATH entry, behind the inherited one. Reporting it as a plain declared variable would
  describe an influence the component does not have; it is reported as what it is, a path
  extension appended after lydite's own.
- A declared key the resolved toolchain also sets is overridden, because the toolchain's variables
  compose last. `GOTOOLCHAIN: auto` in a declaration does not reach the check. Reading it out of
  the YAML would report a steering variable that was cancelled.

This is also why the warning does not live in `internal/component` beside the parse: the parse
knows the declaration and not the composition, and the composition is the fact.

It is scoped to the per-component language checks, which is the whole of the exposure. Semgrep
and the secret scan are root-scoped and run with no declared environment at all, so there is
nothing to say about them. `lydite test` composes the same environment through the same
`childEnv` and is out of scope: it runs the repository's own suite with the repository's own
environment, which nobody reads as an audited claim, and `lydite scan`'s green is the claim #56
is about.

## The standing comment does not carry it

The pull-request comment reports findings — located claims about the code, with anchors and
threads ([`surface.md`](../../agentic/references/surface.md)). A declared environment is a
property of how the run was configured, not a claim about a line, and it has no anchor to hang
on. It would be a permanent block of text on every comment for every repository that declares an
`env:` at all, which is most of them, saying the same thing on every change that does not touch
the declaration.

The audience that needs this is the reviewer of the change that *adds* a variable, and that
reviewer is reading the diff of `.lydite/components.yml` — where the value is too — not the
comment. The CI log is where the run's own conditions belong, and a reader who wants to know what
a given run composed has it there.

This is not the "a section that quietly disappears reads as a concern that passed" case: that
rule governs a section the comment is expected to have and lost. The comment is never expected to
have this one.

## Nothing is refused, and the disqualifier is the enforcement

No variable is rejected, no declaration fails a scan, and there is no allowlist, denylist or
`--strict-env` flag.

A refusal would be the allowlist under another name, and it would break more than it guards.
`component.LoadHistorical` reads `.lydite/components.yml` off a base tree to answer what the
merge-base declared; a refusal there makes a tree that was perfectly valid when it merged
unreadable now, and the commands that need the base — the coverage baseline, the licence delta —
fail on history rather than on the change. A rule that can retroactively invalidate a merged tree
is not a rule a gate can hold.

**The enforcement already exists, and it is deliberate rather than a gap: editing
`.lydite/components.yml` is a built-in referral disqualifier.** Adding a `GOVULNDB` to a
component is therefore a change that cannot merge unattended — it goes to a human, with the
diff showing the variable and its value. That is the correct shape for this: the question "should
this component's govulncheck point at that database" is a judgement about one repository's
circumstances, which a reviewer can make and a list in lydite cannot. The warning makes the same
fact visible on every subsequent run, so a variable that got through review once does not become
invisible afterwards.

## Consequences

- A repository declaring an `env:` sees one warning line per component on every `lydite scan`,
  including the ordinary cases — a Rust component's `SQLX_OFFLINE`, a Go component's
  `CGO_ENABLED`. That is the cost of uniform reporting and is accepted; a warning that appeared
  only for the interesting cases would be the allowlist.
- Nothing about a scan's exit code, its rows or its `--json` document changes. A consumer parsing
  either reads the same bytes.
- The steering-variable list is maintained on a best-effort basis and is expected to lag the tools
  it describes. No test asserts its completeness, because completeness is not a property it
  claims.
- #56 is closed without closing the influence it names. A repository still shapes its own scan
  through its own configuration, as ADR 0020 records it is entitled to. What the scan adds is
  that it says so every time.
- The narrative lives in [`scanning.md`](../../agentic/references/scanning.md); ADR 0020's
  paragraph on the open half carries a note pointing here.
