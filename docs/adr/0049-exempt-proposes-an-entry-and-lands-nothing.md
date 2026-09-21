# `/lydite exempt` proposes an entry and lands nothing

`/lydite exempt <shape>` is the third verb of the comment surface, beside `/lydite clear` and
`/lydite explain`. It answers with a comment containing a paste-ready `.lydite/exemptions.yml`
entry: `name` is the `<shape>` the commenter typed, `paths` is derived from the change's own
uncovered set, and `reason` is a placeholder stating the question a person has to answer. It
writes nothing to the repository, touches no status, and edits no standing comment. Landing the
entry is an ordinary pull request somebody opens, reviews and merges.

[#33](https://github.com/lydite/lydite/issues/33) carries the verb, and
[ADR 0015](0015-clearance-binds-to-a-commit.md) held it back with the reason this ADR settles —
"held back because what it should emit is undecided rather than unbuilt". What it emits is text
addressed to a person. That is the whole of the decision, and every choice below follows from
refusing to let that text act on its own.

The constraint is [ADR 0014](0014-evidence-only-referral-matching.md)'s rule, and it binds here
in a shape it has not had before:

> An author-controlled claim may only ever add a referral, never remove one.

A proposal is not a claim lydite acts on — no code path reads it, and the referral stands
untouched until the exemptions file changes on the default branch. But it is a claim aimed at
the one thing that *can* remove a referral, and it arrives pre-formatted, syntactically valid
and ready to merge. A reviewer skimming a block that already parses is the reader least likely
to re-derive what it covers. So the rule is applied to the proposal as if it were the
exemption: nothing a commenter writes may widen what the block says, and nothing lydite
generates may stand in for the judgement the entry exists to record.

## `<shape>` names the exemption, and lydite derives the paths

The one argument is the entry's `name`. Everything else in the block is either computed from
the change or deliberately left unanswered.

`paths` comes from the change's own uncovered set, which `/lydite exempt` computes for itself:
it asks the forge for the pull request's changed paths, and runs `internal/referral`'s `Covers`
and `uncovered` over that list against the exemptions file in its own checkout of the default
branch — located under `--dir` through `referral.RootRelative`, the way every other command
finds it, so a repository whose scan root is a subdirectory reads the declarations governing it
rather than none at all. The read is off the working tree rather than out of a commit: this
job's checkout is the default branch already, so there is no branch here whose own widening it
could read. `uncovered` answers "which paths would I have to declare?" — weaker than the
all-or-nothing rule `Decide` applies, and exactly the question `/lydite exempt` is asked.

**A commenter-supplied glob was rejected.** It lets the author widen the proposal past what
their own change needs covered: nothing stops `/lydite exempt docs docs/**` on a change that
touched `docs/README.md` and nothing else. That is the author-controlled-claim shape ADR 0014
exists to keep away from anything that can remove a referral, and it is worse here than in a
hand-written entry, because the block reads as lydite's output. The reviewer who would have
interrogated a glob a colleague typed will accept the same glob when a tool appears to have
derived it.

**A derived path is glob-escaped, because a filename is not a pattern.** The same widening
arrives one level down without anybody typing anything: `paths` entries are `internal/pathmatch`
patterns, and a changed file whose real name carries `*`, `?`, `[` or `]` becomes one when it is
copied verbatim. `app/[slug]/page.tsx` — an ordinary Next.js route — proposes a character class
covering `app/s/page.tsx` and missing the file that produced it, and a file git permits to be
named `**` proposes the pattern covering every path in the repository, presented as the paths
this change touches. So `escapeGlob` in `cmd/lydite/clearance.go` prefixes each of those
characters, and the backslash itself, with a backslash before the entry is encoded: `path.Match`,
which `pathmatch.Match` calls per segment, reads a backslash as escaping the rune after it.
A `**` segment needs no exception — it escapes to `\*\*`, which is not the string
`pathmatch` special-cases as the many-segments wildcard, so it stays two literal stars.

**A fixed preset vocabulary was rejected** — `readme-only`, `docs-only`, and whatever else
somebody enumerates. It does not generalise: the next boring change has a shape nobody named,
and the verb then has nothing to say about precisely the case it was asked about. And it is
redundant with deriving from the evidence, which answers every shape without anybody having to
predict it.

**An empty uncovered set refuses rather than proposing.** A change can be referred while
`uncovered` returns nothing: each path is covered by some exemption, no single exemption covers
them all. There is no path list that would make that change exempt — the accurate answer is
"declare this combination", which is a judgement about which shapes belong together and not a
list lydite can derive. Emitting an empty `paths` would fail validation; emitting the change's
full path list would propose an entry duplicating two others, widened by union, which is the
reading ADR 0014 rejected outright. So the reply proposes nothing: it says every path the
change touches is already covered, declines to name which of the two indistinguishable causes
is responsible — paths split across exemptions no single one covers, or a disqualifier vetoing
a match this computation never sees — and points at the standing verdict comment for what is
actually holding the change.

## A list of changed paths is metadata, and fetching it is not fetching content

The comment-time job does not have the path list today. `clearance.Status` carries a state, a
description and a timestamp, and nothing else; `referral.Decision`'s `Uncovered` is computed by
`review` from a checkout's diff and is never persisted anywhere this job could read it back. So
`/lydite exempt` needs a capability `clear` and `explain` do not have: a new `forge.Client`
call — `ChangedPaths`, beside `HeadSHA`, `CanWrite` and `ReferralStatus` — returning the pull
request's changed file names from the platform's own list-files endpoint. A rename contributes
both of its names, the rule ADR 0014 sets for the diff and for the same reason: counting only
the destination lets a file move into or out of an exempt tree with the proposal none the wiser.

**This is not the thing ADR 0015's invariant keeps out.** "No pull-request content is fetched at
comment time" protects a job that holds a writing credential and checks out only the default
branch from ever executing or interpreting what the pull request contains: a build script, a
manifest, a patch, a lockfile whose parser the branch gets to choose the input to. A list of
names is none of that. Nothing is compiled, nothing is parsed beyond the strings themselves,
no tree is materialised, and the branch's control over the value extends exactly as far as
naming files — which is the same control it already has over the diff `review` matches against
in a job built to run it. The paths are also what the platform itself reports, not what the
branch asserts about them, so the evidence is off the same footing ADR 0014 requires: lydite
reads what changed, never what a commenter says changed.

**Reading a report the review job wrote was rejected.** It is the shape clearance already uses
for the status, and `Uncovered` is right there in the decision `review` reaches — but nothing
writes it down, so this would be the artefact-passing mechanism the `versions:` section below
declines to build, arriving through the back door to answer a question a metadata call answers
outright.
It would also make the proposal a function of whenever that artefact was written rather than of
the head the ladder just checked.

**Asking the commenter to paste the paths was rejected** for the reason the glob was: it is the
author choosing what the entry covers, in the one field that decides what merges unread.

The distinction that matters is the one between this and `versions:` below. A path list is
metadata the platform holds about a pull request. Licence and SCA evidence is the output of
running lydite's own gates over the branch's code, and no endpoint returns it.

## The generated `reason` is a question, never a sentence

`Reason` is required by `Exemption.validate`, and the error message says why: it "is what makes
this entry reviewable". A generated entry cannot satisfy that requirement, because the thing
required is a person's judgement. So the placeholder states what the person has to supply:

```yaml
exemptions:
  - name: docs-only
    reason: "TODO(lydite): why is a change touching only these paths safe to merge unread?
      State what this entry's paths guarantee, and nothing the schema does not check."
    paths:
      - docs/adr/0047-an-added-dependency-refers-and-a-version-bump-is-conditionally-exempt.md
      - docs/release-notes/0.2.0.md
```

The block is valid YAML, and it is not yet an exemption: the `reason` is unfinished, visibly to
a reader and — by the subsection below — to `Parse`.

**A templated real-sounding reason was rejected**, and this is the decision with the most
weight behind it. "Changes touching only `docs/**` are documentation-only and carry no runtime
risk" is grammatical, specific-looking and produced in milliseconds, and it is wrong in exactly
the way [#181](https://github.com/lydite/lydite/pull/181) was wrong. That entry — the first real
one in this repository's exemptions file — reached review with a `reason` leaning on
Dependabot's cooldown, which nothing in the schema checks or ever will, and was rewritten in
review to state only what the entry's own fields guarantee. The same review caught a `paths:`
entry naming `Cargo.toml`, a manifest `internal/depdelta.Detect` does not recognise and
therefore skips silently rather than reporting unmeasured: an edit to it alone would have
cleared the exemption with the lockfile beside it never compared. Both defects survived
drafting and were caught by a person reading carefully. A template produces that class of
defect faster and dressed better — prose that already sounds review-quality is prose a skimming
reader accepts, and the requirement that a human do the reviewing is defeated precisely when the
generated text is good enough to pass for having been thought about.

**Leaving `reason` empty was rejected too**, though it is the honest failure: the entry fails
`validate` the moment anybody tries to use it, which is the right outcome. It just teaches
nobody anything. A question-shaped placeholder fails the same way while saying what the author
is expected to supply, and it makes the omission legible in review — an unanswered
`TODO(lydite):` in a diff is a thing a reviewer blocks on, where an absent key is a thing they
may read as a schema they have half-remembered.

### The marker is reserved, and the schema rejects it

A placeholder is only not-paste-ready if something says so. `Exemption.validate` tests
`Reason == ""` and nothing else, so a generated question is a non-empty string and the file
parses: the block above, landed unedited, would be a live exemption. That is the same failure
this ADR spends its opening refusing — a proposal that acts on its own — arriving through the
one field meant to guarantee a person was involved.

So the placeholder's opening literal is reserved. The generator emits exactly `TODO(lydite):`
at the head of the `reason` it writes, and `validate` rejects any exemption whose `reason`
carries that marker anywhere in it, naming why:

> reason still carries lydite's own placeholder marker — an exemption needs a person's
> judgement, not a generated question

Anywhere rather than only at the head, because a reason that keeps the question and prefixes a
sentence to it has answered nothing; and a literal a person would not write by accident, so the
check never fires on a reason somebody meant.

**The check belongs in `internal/referral`, not in the code that generates the comment.** The
generator is one route from a proposal to `.lydite/exemptions.yml` and it is not a route
lydite controls: the block is copied by hand into a pull request, or pasted by whatever the
author's editor or agent does with a fenced code block. `validate` is the one place every route
already passes through — a hand-written file, a generated one, and any future producer nobody
has thought of — so it is the only place that can veto a leftover placeholder regardless of how
it arrived.

**Relying on the wording alone was rejected.** It makes the whole protection a hoped-for norm:
a reviewer skims a field that already reads like a sentence, or an author copies the block,
deletes the marker and leaves "why is a change touching only these paths safe
to merge unread?" standing verbatim as though it were the answer. Both produce a valid file and
a green run. The enforcement is what turns leaving the reason exactly as generated into a hard
failure at parse time, which is the only form of "somebody must actually write this" that does
not depend on somebody actually reading it.

## No `versions:` in a proposal

A proposed entry carries `name`, `paths` and the placeholder `reason`, and never `Versions`.

`versions: patch-and-minor` is backed by `Evidence.PatchAndMinor`, which
[ADR 0047](0047-an-added-dependency-refers-and-a-version-bump-is-conditionally-exempt.md) computes
from a full `review` run: a `depdelta` diff against the base, plus the licence and SCA rows for
every touched component, read out of a scan document. None of that is in the webhook payload the
comment surface reads, and none of it is metadata a forge endpoint holds: the changed-path call
above returns names, and a name says nothing about how far a version moved or whether a licence
gate passed.

**Fetching it live at comment time was rejected.** ADR 0015 states the invariant every existing
comment command rests on: "no pull-request content is fetched at comment time". The clearance
job holds a writing token and checks out the default branch's own copy of lydite, never the pull
request's. Reading a manifest off the head — let alone running a scan against it — hands the
branch's own content to a job with credentials, for a key the verb can do without. This is the
line the previous section draws, falling the other way: names are metadata, a lockfile's
contents are not.

**A precomputed artefact from the upstream job was rejected for this slice, not on principle.**
Clearance already consults a precomputed status rather than recomputing a verdict, and
dependency evidence attached by the `review` job would be the same shape one level along. But it
is a new artefact-passing mechanism — retention, ownership, and what it means when the head moved
after it was written — and nothing in #33 asks this slice to build one. If that mechanism arrives
for some other reason, a `versions:` proposal is a small addition on top of it and gets its own
ADR then. Until it does, a dependency bump gets a paths-only proposal, and whoever lands it adds
the condition by hand in the pull request where it will be reviewed.

## The reply is a new comment, from the job's own token

`/lydite exempt` is a `Verb` alongside `clear` and `explain`, decided by the same `Decide`
ladder, refused by the same reasons in the same order: no write access, unknown verb, a stale
named SHA, no standing `lydite/referral` status on the head, a status published after the
comment. Only past all of those does it derive anything. There is no shortcut for it being the
verb that changes nothing — a proposal derived from a head the commenter never read names the
wrong paths, which is the same failure as clearing code nobody saw, delivered as a suggestion.

The answer is a brand-new `CreateComment`, posted with the clearance job's own ephemeral
`GITHUB_TOKEN`.

**Appending to the standing verdict comment was rejected.** The standing comment carries what is
true of the change and is replaced in place on every push by the review and scan pipeline. A
proposal is an answer to one person's question at one moment; putting it there would answer
where the asker is not looking and overwrite a verdict with a reply to one of them. That is the
reasoning already governing `clear` and `explain`'s replies, and nothing about this verb differs.

**Posting through pr-relay was rejected.** The relay exists for jobs that hold no write
credential of their own. The clearance job holds one, and uses it for the other two verbs today.
A second identity for one verb is a distinction with no capability behind it, and one more
identity to reason about when asking who posted what.

**Writing a commit status was rejected.** Nothing about a proposal resolves a referral. A status
write would say something had been settled by text nobody has read yet, which is the one thing
this whole design is arranged to prevent.

## Consequences

- `internal/clearance` gains `VerbExempt` and `KindExempt`, parsed and decided by the code
  already there. The ladder is not restructured: the new verb is refused by every reason the
  existing ones are.
- `forge.Client` gains `ChangedPaths`, the first call the comment surface makes about the pull
  request itself rather than about a commit's statuses or a commenter's permissions. It returns
  names only, and nothing downstream of it opens a file.
- Deriving `paths` makes the comment surface depend on `internal/referral` for the first time:
  `exempt` loads the exemptions file from its own checkout and runs `Covers` and the uncovered
  computation in-process, which `internal/referral` has to expose rather than keeping to itself.
  Where `clear` and `explain` read a status and decide, `exempt` computes the same coverage
  question `review` does, over a path list rather than a diff.
- `internal/referral.Exemption.validate` gains a check for the reserved `TODO(lydite):` marker,
  beside the `name`, `reason`, `paths` and `versions` checks already there. It is the only
  addition here that constrains a hand-written exemptions file as well as a generated one, and
  that reach is the point of putting it there.
- A referred change whose paths are covered by several exemptions but by none alone gets a
  refusal with an explanation rather than a proposal. That is the ADR 0014 union case surfacing
  at the comment surface, and it stays visible there rather than being rounded off.
- A dependency bump's proposal omits `versions:`, so pasting it unedited declares a wider
  exemption than the one this repository actually wants. The placeholder `reason` is what
  carries the reader to noticing; the block is a draft, and this is one of the things a draft is
  wrong about.
- **This ADR implements none of it.** It records what `/lydite exempt` emits, which is what
  ADR 0015 named as undecided; building the verb closes #33.
