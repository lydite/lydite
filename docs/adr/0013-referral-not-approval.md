# Changes merge unattended only if they match a declared exemption

lydite exists to gate work written by coding agents. That changes the adversary model
underneath every check it already has.

Existing gates assume an author who wants the gate to pass *because they want correct
code*. An agent optimises for the proxy it was given — a green pipeline. Both produce a
passing build; only one produces working software. And a second, general-purpose
detector that used to stand behind every check — a human reading the diff — is exactly
what this arrangement removes.

## Gates and referrals are different things

Every check lydite has splits cleanly once the question is "who can clear this?".

A **gate** is cleared by doing more work: adding an assertion that kills a mutant,
raising patch coverage, decomposing a complex function. An agent iterating against a
gate is doing precisely what was wanted, so gates should be loud, blocking, and the
agent's to clear.

A **referral** cannot be cleared by the author at all. It is resolved by a human, and
no change to the branch resolves it.

The dividing line is not severity. It is whether the signal can be satisfied by *doing
less checking*: a suppression annotation, a skipped test, a widened tolerance, a
disabled language. Those are not measurements of the code — they are evidence that
something tried to make a verdict go away, and they must never be clearable by the
thing that produced them. It follows that the gate always evaluates the **merge-base's**
configuration, never the pull request's: a change that loosens the gate gets no benefit
from its own loosening.

## An allowlist, because the failures are the unanticipated ones

A denylist — merge everything unless it trips a known-dangerous signal — only catches
failure modes someone enumerated. That is a fair bet when a human reads the diff,
because the human catches the rest. Remove the human and the residual risk is precisely
what nobody thought to list.

So the default is to refer, and a change merges unattended only when it matches a
declared **exemption**: README-only, a dependency bump with a clean SCA run. The
exemption set is therefore the entire risk model. There is no riskiness score and no
threshold to tune — a decision that also disposes of the observation that risk
correlates poorly with diff size, since a twelve-line migration and a three-thousand-line
regenerated client are both simply unexempt.

Exemptions must be phrased as "all of this change is boring", never "some of it is".
An exemption matching on *any* changed file is a hole: an agent appends a README tweak
to a dangerous change and takes the unattended path.

Two lists move in opposite directions. Exemptions widen what merges unattended.
**Disqualifiers** — a net-new suppression, a newly skipped test, an edit to lydite's
own configuration or to CI workflows — veto *any* exemption match. Expressing those as
conditions inside each exemption instead would mean every future exemption has to
remember every disqualifier, and forgetting one is silent.

The accepted cost is front-loaded friction: on day one the exemption set is empty and
everything is referred. That is the correct starting state rather than a transitional
one, and each exemption added afterwards is an explicit, versioned statement of what may
merge unread — which is the artefact worth having.

## Clearance is a comment, not an approval

GitHub forbids approving one's own pull request. For a single maintainer that makes
review approval unusable as the signal: work opened under the maintainer's own
credentials can never be approved by anyone. Separating agent and human identities
would restore it, but locally that is configuration rather than a boundary — an agent
holding the maintainer's token *is* the maintainer, and no server-side mechanism
distinguishes them.

Comments have no such restriction, so clearance is a comment. The trust is
conventional at first, and unlike an approval it has somewhere to go: a comment can
carry a code from an authenticator app, which an agent holding the token still cannot
produce. Verification runs from the base branch, so the check and its secret are never
the pull request's to edit.

Because comments have no equivalent of `dismiss_stale_reviews_on_push`, a clearance
names the revision it was given for, and the commit status *is* the record. A push
produces a new head SHA carrying no status, so clearance evaporates without any state
of its own.

Enforcement therefore inverts the usual arrangement: native required-approval count
drops to zero, and lydite's own status check becomes the required one — the same
inversion `palantir/policy-bot` makes, arrived at for the same reason. lydite owns that
loop rather than delegating it, accepting the fail-open bug class that comes with it,
because the loop is the product.

## The honest path is always available, and always routes through a human

Two checks compose with the above in a way worth stating, because it is the property
the whole design is for.

A surviving mutant that cannot be killed must be annotated as equivalent. A public API
break must be declared through the conventional-commit marker. Both annotations are
themselves disqualifiers. So an agent may always proceed — but declaring why lands the
change on a human's desk with the claim attached.

The agent is never stuck, and never has a way around.
