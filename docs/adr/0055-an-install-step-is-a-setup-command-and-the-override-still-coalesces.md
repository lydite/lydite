# An install step is a `setup:` command, and `typescript.install` still coalesces where a root resolves

[#248](https://github.com/lydite/lydite/issues/248) reports a TypeScript component that needs one
command beyond its dependency install — `playwright install --with-deps chromium`, for one component
in a pnpm workspace — and a configuration surface whose only answer is `typescript.install`. That key
is all-or-nothing twice over. `internal/nodedeps.Commands` (`nodedeps.go:188-191`) returns the
override alone, so the install lydite would have detected is gone; and `internal/nodedeps.Install`
(`nodedeps.go:233-240`) skips `WorkspaceRoot` whenever an override is set and runs it in the
component's own directory, so the install is keyed on that directory instead of the workspace root
every sibling component resolves. The repository paid for one extra command with a hand-rolled mutex,
branching on `basename $(pwd)`, and some fifty lines of shell reconstructing the install the override
had discarded.

The issue asks for a way to add a step without discarding the install. That mechanism exists already,
so no key is added. What is wrong is the second short-circuit, and it is wrong independently of the
motivating case: `typescript.install` keeps its meaning — replace the whole install — and stops
opting its component out of coalescing wherever a single workspace root resolves.

## An extra install step is a `setup:` command

`Component.Setup` (`internal/component/component.go:148-150`) runs per component, after the
coalesced node install. `cmd/lydite/test.go`'s `runComponent` calls `prepare` (`test.go:726`), which
reaches `nodedeps.Install` through a runner's `Prepare` or through `prepareCommand`; then
`startServices` (`test.go:733`); then `runCommands` over `c.Setup` (`test.go:751`). By the time a
setup line runs, the workspace's `node_modules` is in place.

It also runs with the environment the step needs. `runCommands` (`test.go:1171-1179`) hands each line
to `sh -c` in the component's directory under `childEnv(tc, c, runner.Invocation{})`
(`test.go:1836`), which carries the toolchain's `PathDirs` — so `npx`, `npm` and `pnpm` resolve to the
Node lydite provisioned — and the component's own declared `env:`. So

```yaml
setup:
  - npx playwright install --with-deps chromium
```

on the one component that needs a browser is the whole answer to #248's case. It runs for that
component and no other, never touches `typescript.install`, and leaves the install every sibling
shares exactly as detection chose it.

The extension case is two components under one workspace that both need the step. The browser
download lands in a cache outside the repository tree (`~/.cache/ms-playwright`), and two concurrent
`playwright install` runs race on it. That is the collision
[ADR 0050](0050-a-component-declares-the-paths-it-occupies.md) exists for — a directory a `setup:`
writes that lydite cannot see — and declaring the shared path in both components' `occupies:` makes
the scheduler run them one after the other. Nothing about an install step needs a lock of its own.

**A dedicated composing key was rejected** — something like `install_extra:`, or a
`typescript.install` mode that appends to detection rather than replacing it — and not only because
`setup:` exists. The new key would need a timing (after the coalesced install), an environment (the
toolchain's `PATH` plus the declared `env:`) and a scope (one component, not the root) that `setup:`
already has, and it would raise questions `setup:` has already answered: whether it lives on
`TypeScriptLanguage` in `.lydite/config.yml`, where it is one value for every component, or on
`Component`, where it is `setup:` under another name; whether a step appended to a coalesced install
runs once per root or once per component; and how two components appending the same step to one root
serialise against each other, which is `occupies:` again. A step composed *into* the coalesced
install also runs once for the whole root under whichever component reached it first, which is the
wrong scope for a step one component needs.

`typescript.install`'s shape and meaning do not change. It replaces the whole install, and nothing
composes with it.

## `Install` still resolves a root when an override is set

`Commands`' short-circuit is correct and stays: an override replaces *what runs*, and there is
nothing to detect once a repository has said what its install is. `Install`'s short-circuit is a
separate decision — *where* it runs, and so what it is coalesced with — and conflating the two is the
defect. Installs are coalesced on the resolved root: `installed` records a root's install once,
`lockRoot` makes a second component wait for the first, and `installedUnder` refuses a second
component that would share an install run under a different environment. Keying an override on the
component's directory instead makes every component with that override its own key, so several of
them run the same install concurrently over one shared `node_modules`, lockfile and store — the rename
race `prepareCommand`'s doc comment describes, reintroduced by a configuration key.

So `Install`, given an override, first calls `WorkspaceRoot(dir, scanRoot)`. When it resolves, the
override runs at that root under the same `lockRoot` and the same `installedUnder` guard as a detected
install — exactly as though detection had chosen that root and the override had supplied the
commands. Only when `WorkspaceRoot` resolves nothing does `Install` run the override in the
component's own directory, keyed there, sharing nothing with any other component.

**Requiring `WorkspaceRoot` to resolve whenever an override is set was rejected.** `WorkspaceRoot`
(`nodedeps.go:92-109`) stops at the nearest directory holding any lockfile and returns `("", false)`
when `Manager` finds more than one recognised lockfile there (`nodedeps.go:100`). That ambiguous root
is one of the two cases `TypeScriptLanguage.Install`'s own doc comment (`internal/config/config.go`)
gives the key for. Requiring a resolved root would make the override unusable in exactly that case,
and the case cannot coalesce anyway: there is no single root to coalesce against, because deciding
which lockfile governs it is what the override is for.

**Leaving `Install`'s override branch as it stands was rejected**, because it keeps the race live in
the common case. A repository with one lockfile at one well-defined root reaches for the override not
because its root is ambiguous but because its *command* is nonstandard — a Corepack-pinned manager,
an install flag detection does not add. Nothing about that repository makes its install
per-component, and running it per component races every sibling.

What changes is `internal/nodedeps.Install`'s override branch (`nodedeps.go:233-240`), which gains the
`WorkspaceRoot` attempt before its fallback to `dir`. `internal/config` and `internal/component` gain
no field. This record is design only; the change lands in its own slice.

## What the override is still needed for is not enumerated here

With an extra step expressed as `setup:` and the override coalescing wherever a root resolves,
`typescript.install` is left with two purposes: the ambiguous multi-lockfile root, which it always
serves, and whichever Corepack-pinned or otherwise nonstandard install flows remain once package-
manager provisioning ([#247](https://github.com/lydite/lydite/issues/247)) lands. #247 has not landed,
and its shape decides which flows still need an override. This record does not list what #247 leaves
behind and does not design against a shape that is not yet known; narrowing the key further is a
question for its own issue once #247's actual shape is settled.

## Defects the implementing slice carries

Two defects #248 names are outside this decision and recorded so the slice that implements it does not
lose them.

- `TypeScriptLanguage.Install`'s doc comment (`internal/config/config.go:77-85`) says the key is "Only
  consulted by coverage (internal/coverage), never by scan". The test path consults it: `prepare`
  passes it to a runner's `Prepare` (`cmd/lydite/test.go:1209`) and `prepareCommand` to
  `nodedeps.Install` (`test.go:1233`). The comment should name where the key is read.
- `installNote` (`cmd/lydite/test.go:1285`) returns no row whenever an override is set, on the
  reasoning that the override runs in the component's own directory and needs no root. That reasoning
  stops holding once the override resolves a root, and the silence is wrong regardless: the report
  says nothing about which install ran precisely when it was the least standard one, and a row that is
  absent reads the same as one that passed. The override case should render a row naming the override
  and where it ran.

## `setup:` is named where a reader looks for how to add a step

A repository reaching for `typescript.install` to add a step does so because that key is the only one
the surface names. `installHint` (`cmd/lydite/test.go:1265`) points at `typescript.install` alone, and
`Component.Setup`'s doc comment describes migrations and fixtures but not an install step. The
implementing slice strengthens both: `Setup`'s comment names an extra install step — with `occupies:`
for a cache two components share — as one of its uses, and `installHint` points at `setup:` for adding
a step and reserves `typescript.install` for replacing the install.

## Consequences

- An override at a resolved root runs there, once per root, rather than once per component in each
  component's own directory. An override that inspects its working directory — the motivating
  repository's `basename $(pwd)` branching — sees the root instead. That is the intent: per-component
  work belongs in `setup:`, and an override is the root's install.
- Two components resolving one root with an override now share a single install, and a mismatch in
  their environments is refused by `installedUnder` as it is for a detected install, instead of passing
  unnoticed as two separate installs.
- An override at an ambiguous root, or under no lockfile at all, behaves as it does today: run in the
  component's own directory, shared with no other component.
- `setup:` for an install step costs one run of the step per component per invocation. A step like
  `playwright install` is idempotent and returns quickly once its cache is warm; a step that is not is
  the author's to make so, as any `setup:` line is.
