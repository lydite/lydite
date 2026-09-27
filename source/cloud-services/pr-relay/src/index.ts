import {
  GITHUB_API,
  OPS_VERSION,
  apiHeaders,
  appJwt,
  applyReview,
  installationId,
  installationToken,
  pullRequestFromRef,
  reviewCommentIds,
  upsertComment,
  verifyActionsToken,
  type ActionsClaims,
  type ReviewOps,
} from "@lydite/github-app";

export interface Env {
  LYDITE_APP_ID: string;
  LYDITE_APP_PRIVATE_KEY: string;
  AUDIENCE: string;
  /**
   * The `job_workflow_ref` values allowed to post each gated context, as exact
   * strings separated by commas or newlines.
   *
   * Exact, because every looser shape is a hole: a prefix match on
   * `lydite/actions/.github/workflows/referral.yml` accepts
   * `...referral.yml.evil@…`, and an entry without its `@<ref>` accepts the
   * workflow at any revision, including a branch anyone with write access can
   * move. A SHA-pinned entry therefore admits that SHA and nothing else.
   *
   * Each defaults to empty, which admits no job at all — the state in which the
   * gated contexts are refused to every caller.
   *
   * `MERGE_GROUP_WORKFLOW_REFS` is named for the run shape rather than for a
   * context, because the job it admits posts `lydite/referral` without being
   * allowed to say what that status reads: `/merge-group` composes the verdict
   * itself from a comparison. A ref in more than one of these lists holds no
   * authority at all.
   */
  REFERRAL_WORKFLOW_REFS?: string;
  CLEARANCE_WORKFLOW_REFS?: string;
  MERGE_GROUP_WORKFLOW_REFS?: string;
}

/** What a client sends. Deliberately small, and none of it is trusted. */
interface CommentRequest {
  /**
   * The pull request to write to. Ordinarily only an assertion to be agreed
   * with the one `ref` names; `resolveTarget` says which runs it actually
   * names the target for, and what is then done to resolve it.
   */
  pull_request?: number;
  marker?: string;
  body?: string;
}

/**
 * The verdict a client asks to be recorded on a revision.
 *
 * The context is the client's, not lydite's to assume: a caller that names its
 * own check is the one deciding which required check this satisfies. Nothing
 * here is defaulted, because a status posted under a context or a state the
 * caller did not ask for is a green tick nobody wrote.
 */
interface StatusRequest {
  state?: string;
  context?: string;
  description?: string;
  sha?: string;
}

/**
 * What a merge-queue entry submits for comparison.
 *
 * None of it is authority. The repository is the claim's, the pull request is
 * derived from the claim's own ref and resolved live before it is read from,
 * and the fingerprint is evidence the relay compares rather than a verdict it
 * is told — a job that could name the state would need no comparison at all.
 */
interface QueueRequest {
  /** The queue ref this entry builds on, which has to agree with the claim's. */
  queue_ref?: string;
  /** The originating pull request, which has to agree with the ref's own. */
  pull_request?: number;
  /** The queue revision the verdict is published against. */
  sha?: string;
  /** The revision the decision was recomputed against, for a person to read. */
  base_sha?: string;
  /** The fingerprint of the reasons the recomputed decision refers on. */
  fingerprint?: string;
  /**
   * Whether the recomputed decision refers at all.
   *
   * False is the ordinary case — a change every exemption covers, tripping no
   * disqualifier — and there is no clearance for it to carry forward, because a
   * clearance is only ever given against a referral. Absent is not false: a
   * submission that does not say goes through the comparison, whose worst answer
   * is a `pending` a person can clear.
   */
  referred?: boolean;
}

const JWKS = "https://token.actions.githubusercontent.com/.well-known/jwks";

const QUEUE_ROUTE = "/merge-group";

const ROUTES = ["/comment", "/review", "/status", QUEUE_ROUTE];

// The two contexts a job may only post from an allowlisted workflow: the
// referral verdict a merge is gated on, and the status a human Clearance acts
// on. They are distinct names so that one workflow's authority to post its own
// is never authority to post the other.
const REFERRAL_CONTEXT = "lydite/referral";
const CLEARANCE_CONTEXT = "lydite/clearance";

// The login GitHub attributes a write made with this App's installation token
// to: an App's bot user, `<app-slug>[bot]`, for the App `LYDITE_APP_ID` names.
//
// `/merge-group` reads a clearance rather than being told one, and a context name
// is not evidence of who wrote it: any token holding `statuses: write` can post
// under `lydite/clearance`, and the fingerprint in the description is a hash of
// the change's own diff (`internal/referral.Fingerprint`) that anybody can
// recompute. Without this check an author forges the status their own entry is
// then cleared by, which is the whole of what CLEARANCE_WORKFLOW_REFS exists to
// stop. A creator is compared and not a token scope, because the read answers
// only what stands on the revision.
//
// Login alone settles it: GitHub permits no `[` in a user or organisation login,
// so no account other than this App's bot can carry this one. A clearance posted
// through the `github-token` fallback rather than through this relay is therefore
// not one a queue entry can carry forward — it is `github-actions[bot]`'s
// assertion, not lydite's.
const APP_STATUS_CREATOR = "lydite[bot]";

// The two referral states this relay reasons about by name, spelled as
// `internal/clearance`'s own `State` values: a standing referral waiting on a
// person, and the verdict a clearance resolves it to. Every other state is a
// verdict this route never authors and never moves.
const REFERRAL_PENDING = "pending";
const REFERRAL_RESOLVED = "success";

// The platform's cap on a status description, which is also what
// `internal/clearance`'s own `DescriptionLimit` composes against: text past it
// is clipped by the platform, and a clipped description loses its tail.
const DESCRIPTION_LIMIT = 140;

// The delimiters `internal/clearance`'s `WithFingerprint` writes a decision's
// fingerprint under, and `FingerprintIn` reads it back from. Two
// implementations of one encoding, so they are spelled the same way in both:
// see `fingerprintOpen`/`fingerprintClose` in
// `source/cli/internal/clearance/decide.go`.
const FINGERPRINT_OPEN = " [fp:";
const FINGERPRINT_CLOSE = "]";

/**
 * What the relay talks to.
 *
 * Injected rather than reached for, because the two things this Worker does —
 * verify a signature against the issuer's published keys, and act on GitHub's
 * answers — are the two things its tests have to drive. A handler that calls
 * global `fetch` directly can only be tested against the real internet, which
 * means the trust boundary is the one part of it nothing covers.
 */
export interface Deps {
  fetchJwks: () => Promise<{ keys: never[] }>;
  fetcher: typeof fetch;
}

const production: Deps = {
  fetchJwks: () => fetch(JWKS).then((response) => response.json() as Promise<{ keys: never[] }>),
  fetcher: fetch,
};

/**
 * The relay.
 *
 * It exists so that a CI job can have lydite write to a pull request without
 * holding a credential that could write anywhere. The job presents the OIDC
 * token GitHub minted for that run; the relay checks it, decides from the
 * verified claims which repository and which pull request may be written to,
 * mints an installation token narrowed to exactly that, writes, and discards it.
 * The repository is always the claim's. The pull request is too, except for the
 * clearance runs `resolveTarget` describes, which have no pull-request ref and
 * whose submitted number is resolved through that token before it is written to.
 * A merge-queue run has no pull-request ref either, and `queueTarget` reads the
 * number out of the one ref shape that carries it instead.
 *
 * Four endpoints, and each writes to one pull request's own record and nothing
 * else. `POST /comment` upserts the standing comment; `POST /review` applies the
 * operations document `lydite threads` computed — the threads to open on the
 * change's own lines, the ones to answer, and the ones to take down; `POST
 * /status` records a verdict on a revision of that pull request, so the check a
 * repository requires is authored by the App rather than by whichever token the
 * job happened to hold. The delta behind the review document is the CLI's, and
 * so is the vocabulary of a status: this decides nothing about which thread
 * belongs to which finding, nor what a state or a context means, which is what
 * keeps lydite's vocabulary in one place rather than in a Worker one release
 * behind it.
 *
 * `POST /merge-group` is the one route that composes a verdict rather than
 * relaying one. A merge queue replays the change onto a fresh base, so the
 * revision it builds carries no clearance and can be given none — its ref
 * belongs to no pull request, so there is no surface a clearing comment could be
 * typed at. The entry submits the decision recomputed on the queue's own tree:
 * whether it refers at all, and the fingerprint of the reasons it refers on. An
 * entry that refers for nothing is published `success` on its own merits, since
 * no clearance was ever given for it; for every other entry the relay resolves
 * the originating pull request live, checks that the queue commit holds that
 * pull request's change and nothing batched beside it, reads the fingerprint the
 * clearance recorded on that pull request's real head, compares, and publishes
 * the referral at the queue revision. The job that submits therefore still holds
 * no writing token, which is the reason the relay exists at all.
 *
 * That covers those four writes and nothing else. The coverage gate runs the
 * repository's own tests and its `setup`/`teardown` shell, and on a pull
 * request that code is the pull request's; with no writable token in the job,
 * the worst that code can do through the relay is provoke a wrong comment on
 * its own pull request — where the change's author is already the person being
 * addressed. Recording a coverage baseline is a different write: it pushes to
 * the `lydite` branch and needs `contents: write`, which this relay never mints
 * and has no endpoint for, so a job that records still holds a pushing token of
 * its own.
 *
 * It stores nothing. No database, no cache, no log of a request body: a body is
 * a rendered comment about somebody's private repository, and the argument for
 * lydite holding none of your data is not one to weaken for a log line.
 */
export function createRelay(deps: Deps = production) {
  return { fetch: (request: Request, env: Env) => handle(request, env, deps) };
}

export default createRelay();

async function handle(request: Request, env: Env, deps: Deps): Promise<Response> {
  const route = new URL(request.url).pathname;
  if (request.method !== "POST" || !ROUTES.includes(route)) {
    return json(404, {
      error: `POST /comment, POST /review, POST /status or POST ${QUEUE_ROUTE}`,
    });
  }

  const presented = bearer(request);
  if (!presented) {
    return json(401, { error: "no Actions OIDC token was presented" });
  }

  let claims: ActionsClaims;
  try {
    claims = await verifyActionsToken(presented, env.AUDIENCE, deps.fetchJwks);
  } catch {
    // No detail. A verifier that says which check failed tells a caller how
    // to get closer, one attempt at a time.
    return json(401, { error: "the Actions OIDC token did not verify" });
  }

  const payload = (await request.json().catch(() => ({}))) as CommentRequest &
    ReviewOps &
    StatusRequest &
    QueueRequest;

  if (route === QUEUE_ROUTE) {
    const error = queuePayloadError(payload);
    if (error) {
      return json(400, { error });
    }
  }

  const target =
    route === QUEUE_ROUTE
      ? queueTarget(claims, payload, env)
      : resolveTarget(route, claims, payload, env);
  if ("error" in target) {
    return json(403, { error: target.error });
  }
  const pull = target.pull;

  if (route === "/comment" && (!payload.body || !payload.marker)) {
    return json(400, { error: "a marker and a body are required" });
  }
  if (route === "/status") {
    const error = statusPayloadError(payload);
    if (error) {
      return json(400, { error });
    }
    const refused = statusAuthorityError(payload, claims, env);
    if (refused) {
      // 403 and not 400: the payload is well formed, and what is refused is
      // the job asking. Deterministic either way — the same job retrying
      // answers the same — so a transport sorting this answer has nothing to
      // fall back for.
      return json(403, { error: refused });
    }
  }
  if (route === "/review" && payload.version !== OPS_VERSION) {
    // Refused whole rather than half-applied. A document from a newer lydite
    // may mean something this does not know, and applying the operations it
    // recognises would leave a pull request in a state neither side asked
    // for.
    return json(400, { error: `this relay applies version ${OPS_VERSION} operation documents` });
  }

  try {
    const jwt = await appJwt(env.LYDITE_APP_ID, env.LYDITE_APP_PRIVATE_KEY);
    const installation = await installationId(jwt, claims.repository, deps.fetcher);
    if (installation === undefined) {
      // An answer, not a failure. The client's next move is its own token,
      // and telling it so is what makes the fallback a designed path rather
      // than an error being papered over.
      return json(409, {
        error: "the lydite app is not installed on this repository",
        fallback: "post with the workflow's own token",
      });
    }
    const token = await installationToken(jwt, installation, claims.repository, deps.fetcher);

    if (route === QUEUE_ROUTE) {
      const outcome = await queueOutcome(token, claims.repository, pull, payload, deps.fetcher);
      if ("error" in outcome) {
        return json(403, outcome);
      }
      await postStatus(
        token,
        claims.repository,
        { ...outcome, context: REFERRAL_CONTEXT, sha: payload.sha },
        deps.fetcher,
      );
      return json(200, outcome);
    }

    if (route === "/comment") {
      if (target.submitted) {
        // A number out of the body is the caller's assertion, and nothing is
        // written under it until GitHub has answered for it. `/status` binds
        // its own write on the head sha below; a comment has no such anchor,
        // so this is where the submitted number is resolved.
        const refused = await submittedPullRefusal(token, claims.repository, pull, deps.fetcher);
        if (refused) {
          return json(403, { error: refused });
        }
      }
      const outcome = await upsertComment(
        token,
        claims.repository,
        pull,
        payload.marker as string,
        payload.body as string,
        deps.fetcher,
      );
      return json(200, { repository: claims.repository, pull_request: pull, comment: outcome });
    }

    if (route === "/status") {
      // The pull request's head, not `claims.sha`: on a `pull_request` run
      // that claim is the platform's synthetic merge commit, which exists on
      // no branch and which no verdict is ever published against — the same
      // reason `forge.PullRequestEvent` reads a head from the event payload
      // rather than from `GITHUB_SHA`. Resolving it here, with the token
      // rather than trusting the body, is what lets `sha` be checked at all —
      // and it is what resolves a submitted number too, since a verdict on a
      // pull request this repository does not have is a pull request `GET
      // /pulls/:n` answers nothing for.
      const pr = await pullRequest(token, claims.repository, pull, deps.fetcher);
      if (!pr) {
        return json(403, { error: NO_SUCH_PULL });
      }
      const head = pr.head?.sha;
      if (!head || payload.sha !== head) {
        return json(403, { error: "the sha submitted is not this pull request's head" });
      }
      if (referralResolution(payload, claims, env)) {
        // The one thing a clearance ref may do to the referral context, and
        // the whole of what admits it: the status this revision is actually
        // carrying, read with the installation token rather than taken from
        // the body. A `failure` is the isolation gate and a comment does not
        // resolve it; an `error` never reached a verdict, so there is none to
        // resolve — and a revision carrying no referral at all has nothing
        // standing that a clearance is answering.
        //
        // The read and the write below are two separate requests, so a
        // referral re-run landing `failure` on this exact head in the gap
        // between them is not caught — the platform's status API has no
        // conditional write to close that window with. It is narrower than
        // what stands today regardless: the direct-post route (the clearance
        // flow's `RecordStatuses` stage, in
        // `source/cli/internal/stages/clearance/statuses.go`) resolves
        // `lydite/referral` to `success` after a clearance with no live read
        // at all, so this is a tighter check on the same exposure rather
        // than a new one.
        const standing = await standingStatus(
          token,
          claims.repository,
          head,
          REFERRAL_CONTEXT,
          deps.fetcher,
        );
        if (standing?.state !== REFERRAL_PENDING) {
          return json(403, {
            error: `a clearance resolves a ${REFERRAL_PENDING} ${REFERRAL_CONTEXT}, and this revision carries ${
              standing?.state ?? "none"
            }`,
          });
        }
      }
      await postStatus(token, claims.repository, payload, deps.fetcher);
      return json(200, {
        repository: claims.repository,
        pull_request: pull,
        status: "posted",
      });
    }

    // Every id the document names has to belong to this pull request, and
    // the whole request is refused if one does not. A comment id is a number
    // the caller supplies while `ref` is the one thing a run cannot choose,
    // so this is what stops a run deleting a comment on somebody else's pull
    // request — and refusing the whole document rather than the operation
    // means a caller cannot use the answer to probe which ids exist.
    const members = await reviewCommentIds(token, claims.repository, pull, deps.fetcher);
    const named = [
      ...(payload.reply ?? []).map((op) => op.comment),
      ...(payload.delete ?? []).map((op) => op.comment),
    ];
    if (named.some((id) => typeof id !== "number" || !members.has(id))) {
      return json(403, { error: "the operations name a comment that is not on this pull request" });
    }

    const outcomes = await applyReview(token, claims.repository, pull, payload, deps.fetcher);
    return json(200, { repository: claims.repository, pull_request: pull, outcomes });
  } catch {
    // The reason is deliberately not relayed. It is about lydite's own
    // credentials and GitHub's answers to them, neither of which is the
    // caller's to see.
    return json(502, { error: UNWRITTEN[route] });
  }
}

// Each endpoint names its own write, so a reader of a job log is not told the
// comment failed when the status did.
const UNWRITTEN: Record<string, string> = {
  "/comment": "the comment could not be posted",
  "/review": "the review could not be applied",
  "/status": "the status could not be posted",
  [QUEUE_ROUTE]: "the queue entry's verdict could not be published",
};

/**
 * The one pull request a request may write to, or why there is none.
 *
 * `submitted` travels with the number because it decides what still has to be
 * checked: a number taken from `ref` is already GitHub's own answer, while a
 * number taken from the body is the caller's assertion and is resolved live
 * before anything is written under it.
 */
type Target = { pull: number; submitted: boolean } | { error: string };

/**
 * Where the pull request comes from.
 *
 * `ref` is the only claim a run cannot choose, so wherever it names a pull
 * request it names the one being written to, and a submitted number has only to
 * agree with it. A clearance run has no such ref: `lydite-clearance.yml` runs on
 * `issue_comment`, whose token is minted for the default branch, so the pull
 * request the comment was left on appears in no claim at all and the run has
 * nothing to write to unless it may say which.
 *
 * That exemption is keyed on the verified claims `clearanceRun` reads — the
 * clearance allowlist naming the `job_workflow_ref`, and the run presenting the
 * event and the ref shape a clearance run has — and on nothing in the body: a
 * request cannot ask for it. Every other run, an allowlisted referral workflow
 * included, keeps deriving the pull request from its ref and is refused when
 * that ref names none. `/review` is outside the exemption too: a clearance run has no
 * operations document, and the ids in one are checked against the pull request
 * the ref named.
 */
function resolveTarget(
  route: string,
  claims: ActionsClaims,
  payload: CommentRequest,
  env: Env,
): Target {
  const fromRef = pullRequestFromRef(claims.ref);
  if (fromRef) {
    if (payload.pull_request !== undefined && payload.pull_request !== fromRef) {
      return { error: "the pull request submitted is not the one this run is for" };
    }
    return { pull: fromRef, submitted: false };
  }
  if (route !== "/review") {
    const authority = clearanceRun(claims, env);
    if (authority.error) {
      return { error: authority.error };
    }
    if (authority.run) {
      const named = payload.pull_request;
      if (typeof named !== "number" || !Number.isInteger(named) || named <= 0) {
        return { error: "this run's ref names no pull request, so a pull request number is required" };
      }
      return { pull: named, submitted: true };
    }
  }
  return { error: "this run is not for a pull request, so there is nothing to write to" };
}

/** Whether a run holds clearance authority, and why an allowlisted ref does not. */
interface ClearanceAuthority {
  run: boolean;
  error?: string;
}

// What `lydite-clearance.yml` runs as: an `issue_comment` on the default
// branch, which is the only shape a clearance run ever presents.
const CLEARANCE_EVENT = "issue_comment";
const BRANCH_REF = "refs/heads/";

/**
 * Whether this run is a clearance run: one whose `job_workflow_ref` the
 * clearance allowlist names and the referral allowlist does not, running as
 * `issue_comment` on a branch ref.
 *
 * The allowlist names a workflow file, and a workflow file says nothing about
 * what started it. The same allowlisted callee reached from a `push`, a
 * `workflow_dispatch` or a same-repository `pull_request` caller would otherwise
 * hold the two things that separate a clearance run from every other job —
 * naming any pull request it likes in the body, and posting the status a human
 * Clearance acts on — from a run triggered by whoever can open a pull request.
 * `event_name` and `ref` are claims the requester cannot set, so they are what
 * that authority is conditioned on, and an allowlisted ref presenting any other
 * shape is refused rather than read as an ordinary run.
 *
 * A ref in both lists holds neither authority. `statusAuthorityError` refuses it
 * outright as the deployment mistake it is, and reading it as a clearance run
 * here would hand a referral workflow — which runs beside the pull request's own
 * code — the one thing that separates the two.
 */
function clearanceRun(claims: ActionsClaims, env: Env): ClearanceAuthority {
  const named = claims.job_workflow_ref ?? "";
  if (
    !allowlisted(env.CLEARANCE_WORKFLOW_REFS, named) ||
    allowlisted(env.REFERRAL_WORKFLOW_REFS, named)
  ) {
    return { run: false };
  }
  if (claims.event_name !== CLEARANCE_EVENT || !claims.ref.startsWith(BRANCH_REF)) {
    return {
      run: false,
      error: `a clearance run is an ${CLEARANCE_EVENT} run on a ${BRANCH_REF} ref, and this run is ${
        claims.event_name || "an unnamed event"
      } on ${claims.ref || "no ref"}`,
    };
  }
  return { run: true };
}

/**
 * What is wrong with a `/status` payload, or nothing.
 *
 * Everything about the document itself lives here — the fields the caller must
 * supply and the namespace its `context` must stay inside. Which job may post
 * which context is `statusAuthorityError`'s; `sha` against the pull request's
 * real head is neither, because that check needs the installation token and so
 * stays in `handle`, beside the write it gates.
 */
function statusPayloadError(payload: StatusRequest): string | undefined {
  if (!payload.state || !payload.context || !payload.description || !payload.sha) {
    return "a state, a context, a description and a sha are required";
  }
  if (!payload.context.startsWith("lydite/")) {
    // A context is a check's name, and this is the App's identity to spend.
    // Confined to lydite's own namespace, the worst a caller can do is author
    // a wrong lydite check — never stand in for a check that belongs to
    // another tool.
    return "a status context must start with lydite/";
  }
  return undefined;
}

/**
 * Why this job may not post this context, or nothing.
 *
 * `repository` and `ref` say which pull request is being written to; neither
 * says which job is asking, and the gated contexts are exactly the ones where
 * that matters. A workflow holding `id-token: write` and running the pull
 * request's own code is indistinguishable, by those two claims alone, from the
 * isolated job that runs no such code — so the verdict a merge is gated on, and
 * the status a human Clearance acts on, are admitted only from a
 * `job_workflow_ref` named in the allowlist for that context.
 *
 * The pairing is exclusive in both directions, with one narrow exception. An
 * allowlisted referral workflow may post the referral context and nothing else;
 * a clearance workflow may post the clearance context outright, and may
 * additionally ask to move the referral context from `pending` to `success` —
 * `referralResolution`'s condition, and nothing wider. That attempt is admitted
 * here only as an attempt: what it actually turns on is the status standing on
 * the revision, which needs the installation token and so is read in `handle`.
 * A job whose ref is in neither list is unaffected outside the gated contexts:
 * the `lydite/` namespace check is the whole of what governs the rest.
 *
 * The clearance ref's authority is the whole of `clearanceRun`'s condition and
 * not the allowlist alone: an allowlisted callee reached from a `push` or a
 * `pull_request` caller is a run whoever can open a pull request controls, and
 * the status a human Clearance acts on is not theirs to author.
 */
function statusAuthorityError(
  payload: StatusRequest,
  claims: ActionsClaims,
  env: Env,
): string | undefined {
  const context = payload.context as string;
  const named = claims.job_workflow_ref ?? "";
  const referral = allowlisted(env.REFERRAL_WORKFLOW_REFS, named);
  const clearance = allowlisted(env.CLEARANCE_WORKFLOW_REFS, named);
  if (referral && clearance) {
    // One ref cannot hold both authorities, and resolving the ambiguity toward
    // either would grant an authority nobody wrote down. It is a deployment
    // mistake, refused rather than interpreted.
    return `${named} is allowlisted for both ${REFERRAL_CONTEXT} and ${CLEARANCE_CONTEXT}`;
  }
  if (clearance) {
    const authority = clearanceRun(claims, env);
    if (authority.error) {
      return authority.error;
    }
  }
  const permitted = referral ? REFERRAL_CONTEXT : clearance ? CLEARANCE_CONTEXT : undefined;
  if (permitted) {
    if (context === permitted || referralResolution(payload, claims, env)) {
      return undefined;
    }
    return `${named} may post ${permitted} only`;
  }
  if (context === REFERRAL_CONTEXT || context === CLEARANCE_CONTEXT) {
    // Naming the ref is what makes this actionable: an allowlist is edited by
    // pasting the exact string a refusal quoted, and a SHA pin is a character
    // difference nobody reads out of a job log otherwise.
    return `${context} is posted only by an allowlisted workflow, and ${
      named || "this run names no job_workflow_ref"
    } is not one`;
  }
  return undefined;
}

/**
 * Whether this request is the one move a clearance run may make on the referral
 * context: `pending` to `success`, and nothing else.
 *
 * It exists because the alternative is the required check being unsatisfiable
 * from the relay route at all — a clearance ref may post `lydite/clearance`
 * only, so a `/lydite clear` on a relay consumer would record a clearance
 * nothing acts on, and a repository requiring `lydite/referral` would stay
 * blocked after every clearance, forever.
 *
 * One-directional in both senses. The state has to be `success`: a clearance
 * never authors a `failure`, an `error` or a fresh `pending` under a context
 * whose verdict is another ref's to reach, so any other state is refused
 * outright and the standing status is never even read. And the authority is the
 * whole of `clearanceRun`'s condition, not the allowlist alone, so a referral
 * workflow — which runs beside the pull request's own code — never reaches this
 * at all. Ordering against `lydite/clearance` is the caller's: one `/status`
 * request writes one context, and the relay holds no state with which to know
 * what a previous request did.
 */
function referralResolution(payload: StatusRequest, claims: ActionsClaims, env: Env): boolean {
  return (
    payload.context === REFERRAL_CONTEXT &&
    payload.state === REFERRAL_RESOLVED &&
    clearanceRun(claims, env).run
  );
}

// What a merge-queue run presents: the `merge_group` event, on the queue ref
// the platform builds the entry under. `forge.QueueRefPrefix` is the same
// prefix on the CLI side.
const MERGE_GROUP_EVENT = "merge_group";
const QUEUE_REF_PREFIX = `${BRANCH_REF}gh-readonly-queue/`;

// The segment of a queue ref that names one entry. The trailing revision is the
// base branch's tip the entry was replayed onto and never the pull request's
// head — the head is resolved live, and that base is what the queue commit's own
// change is measured from. Mirrors `queueEntryPattern` in
// `source/cli/internal/forge/event.go`.
const QUEUE_ENTRY = /^pr-(\d+)-([0-9a-fA-F]{7,40})$/;

/**
 * What is wrong with a `/merge-group` payload, or nothing.
 *
 * Every field naming the entry is required because every one of them is
 * compared: a submission missing one is a comparison that cannot be made, and a
 * comparison that cannot be made must be refused rather than resolved either
 * way. `referred` is the one optional field — absent is the comparison, which is
 * the answer that needs the fields above it.
 */
function queuePayloadError(payload: QueueRequest): string | undefined {
  if (!payload.queue_ref || !payload.sha || !payload.fingerprint) {
    return "a queue_ref, a sha and a fingerprint are required";
  }
  const named = payload.pull_request;
  if (typeof named !== "number" || !Number.isInteger(named) || named <= 0) {
    return "a pull request number is required";
  }
  if (payload.referred !== undefined && typeof payload.referred !== "boolean") {
    // Refused rather than coerced. Every value but `false` is read as "refers",
    // so a string or a number would resolve toward the comparison and read as a
    // submission that had said so.
    return "referred is a boolean when it is sent at all";
  }
  return undefined;
}

/**
 * The pull request a merge-queue entry is for, or why there is none.
 *
 * The number comes from the claim's own ref, which is the one thing a run cannot
 * choose, and the body's has only to agree with it — so `submitted` is false:
 * there is no assertion left to resolve past this, and the head the clearance is
 * read off is resolved live regardless.
 *
 * `sha` is the queue revision the verdict lands on, so it names what gets
 * written to and is checked against `claims.sha` rather than trusted. On a
 * `merge_group` run that claim is the revision the queue built, which is exactly
 * the revision a verdict about this entry belongs on — unlike a `pull_request`
 * run, whose `claims.sha` is a synthetic merge commit no verdict is published
 * against.
 */
function queueTarget(claims: ActionsClaims, payload: QueueRequest, env: Env): Target {
  const authority = queueRun(claims, env);
  if (authority.error) {
    return { error: authority.error };
  }
  if (payload.queue_ref !== claims.ref) {
    return { error: "the queue ref submitted is not the one this run is for" };
  }
  const fromRef = queuePullRequest(claims.ref);
  if (!fromRef) {
    return { error: `${claims.ref} names no pull request, so there is no clearance to read` };
  }
  if (payload.pull_request !== fromRef) {
    return { error: "the pull request submitted is not the one this queue ref names" };
  }
  if (!claims.sha || payload.sha !== claims.sha) {
    return { error: "the sha submitted is not the revision this run is for" };
  }
  return { pull: fromRef, submitted: false };
}

/**
 * Whether this run is a merge-queue run: one whose `job_workflow_ref` the
 * merge-group allowlist names and no other allowlist does, running as
 * `merge_group` on a queue ref.
 *
 * Conditioned on the same three claims a clearance run is, and for the same
 * reason: the allowlist names a workflow file, and a file says nothing about
 * what started it. The authority this route carries is narrower than a
 * clearance's — the state is the relay's own to compose, never the caller's —
 * but the same allowlisted callee reached from a `push` or a same-repository
 * `pull_request` caller is a run whoever can open a pull request controls, and
 * a verdict published under lydite's identity is not theirs to provoke.
 *
 * A ref in more than one allowlist holds no authority at all. Reading it as a
 * queue run would hand a referral or clearance workflow this route's own
 * resolution of a pull request from a ref shape it does not present.
 */
function queueRun(claims: ActionsClaims, env: Env): ClearanceAuthority {
  const named = claims.job_workflow_ref ?? "";
  if (!allowlisted(env.MERGE_GROUP_WORKFLOW_REFS, named)) {
    return {
      run: false,
      error: `${QUEUE_ROUTE} is answered only for an allowlisted workflow, and ${
        named || "this run names no job_workflow_ref"
      } is not one`,
    };
  }
  if (
    allowlisted(env.REFERRAL_WORKFLOW_REFS, named) ||
    allowlisted(env.CLEARANCE_WORKFLOW_REFS, named)
  ) {
    return { run: false, error: `${named} is allowlisted for more than one authority` };
  }
  if (claims.event_name !== MERGE_GROUP_EVENT || !claims.ref.startsWith(QUEUE_REF_PREFIX)) {
    return {
      run: false,
      error: `a queue run is a ${MERGE_GROUP_EVENT} run on a ${QUEUE_REF_PREFIX} ref, and this run is ${
        claims.event_name || "an unnamed event"
      } on ${claims.ref || "no ref"}`,
    };
  }
  return { run: true };
}

/**
 * The entry a queue ref names: the pull request, and the base branch tip the
 * platform replayed the group onto.
 *
 * The last segment is the one read: a base branch may hold slashes, and a group
 * carrying several pull requests names the one the platform put last. That is why
 * the base travels with the number — the number alone speaks for one entry of a
 * group, and `queueBatching` measures the queue commit's own change from this
 * base to establish that the group is one entry at all.
 */
function queueEntry(ref: string): { pull: number; base: string } | undefined {
  if (!ref.startsWith(QUEUE_REF_PREFIX)) {
    return undefined;
  }
  const segments = ref.slice(QUEUE_REF_PREFIX.length).split("/");
  const match = QUEUE_ENTRY.exec(segments[segments.length - 1] ?? "");
  if (!match?.[1] || !match[2]) {
    return undefined;
  }
  const number = Number(match[1]);
  // Pull requests are numbered from 1, so 0 names none — and a ref carrying
  // more digits than a double holds exactly names none either.
  if (!Number.isSafeInteger(number) || number === 0) {
    return undefined;
  }
  return { pull: number, base: match[2] };
}

/** The pull request a queue ref names, or nothing. */
function queuePullRequest(ref: string): number | undefined {
  return queueEntry(ref)?.pull;
}

/** A state and the description published beside it. */
interface Verdict {
  state: string;
  description: string;
}

/**
 * The verdict this queue entry earns, or why there is no pull request for it to
 * earn one against.
 *
 * Three answers, ordered by what each needs to establish. An entry whose
 * recomputed decision refers for nothing is published `success` and reads
 * nothing: a clearance is only ever given against a referral, so there is none
 * to compare against and waiting for one is the deadlock ADR 0053 removes, hit
 * by the commonest change there is. That answer speaks for the whole queue
 * commit even when the platform batched it, because the decision behind it was
 * recomputed over the queue's own tree against the base tip — every batched
 * change is inside the diff it was taken over.
 *
 * A referred entry is compared, and the comparison is against one pull request's
 * clearance, so `queueBatching` has to rule out a batch before the clearance is
 * read at all.
 */
async function queueOutcome(
  token: string,
  repository: string,
  pull: number,
  payload: QueueRequest,
  fetcher: typeof fetch,
): Promise<Verdict | { error: string }> {
  if (payload.referred === false) {
    return {
      state: REFERRAL_RESOLVED,
      description: clip(`#${pull} is not referred, so this entry merges unattended`),
    };
  }
  // The pull request's own head, live: the queue ref's trailing revision is the
  // base tip the entry was replayed onto, not a head, and nothing in the body is
  // a revision the clearance may be read off. A pull request this repository does
  // not have is a pull request `GET /pulls/:n` answers nothing for.
  const pr = await pullRequest(token, repository, pull, fetcher);
  const head = pr?.head?.sha;
  if (!head) {
    return { error: NO_SUCH_PULL };
  }
  const batched = await queueBatching(
    token,
    repository,
    payload.queue_ref as string,
    payload.sha as string,
    head,
    fetcher,
  );
  if (batched) {
    return { state: REFERRAL_PENDING, description: clip(batched) };
  }
  const cleared = await standingStatus(token, repository, head, CLEARANCE_CONTEXT, fetcher);
  return queueVerdict(cleared, payload.fingerprint as string, pull);
}

/**
 * Why this queue entry may not be compared against one pull request's clearance,
 * or nothing when it carries that pull request's change alone.
 *
 * The platform may fold several entries into one queue commit, and the ref names
 * only the last of them. A clearance read off that pull request's head then
 * stands in for content its clearer never saw whenever the earlier entries'
 * referral reasons are a subset of the last's: `referral.Fingerprint` hashes the
 * set of uncovered paths and the set of (Kind, Path) disqualifications, so the
 * group's fingerprint equals the last entry's own and the comparison agrees. The
 * queue commit's change is therefore measured against the pull request's own, and
 * anything wider is refused before the clearance is read.
 *
 * The changed files are compared and not the commits: the queue's merge method
 * decides whether a commit survives the replay at all — squash folds the change
 * into one new commit, rebase rewrites every commit sha — while the tree the entry
 * produces has to be this pull request's change and nothing else whichever method
 * built it.
 *
 * Each file's blob sha is compared alongside its path, because the paths alone do
 * not identify a change. An earlier entry touching only files the last pull
 * request also touches produces the same path list at different content, and the
 * fingerprint agrees there too — it hashes paths and disqualification kinds, never
 * what a line says — so a path-only comparison carries the clearance onto content
 * its clearer never saw for precisely the batch it is meant to catch. Two
 * comparisons agree when they name the same files at the same blobs, and nothing
 * weaker.
 *
 * Both comparisons are three-dot and from the base tip the ref's own name
 * carries, which is the revision the platform replayed the group onto. The body's
 * `base_sha` is the caller's own assertion, and a caller choosing the base its
 * batch is measured from is a caller choosing the answer — it would only have to
 * name its own entry's first parent for a group of any size to measure as one
 * change.
 *
 * A comparison that answers nothing usable is refused too, because a batch that
 * could not be checked is not a batch that was ruled out.
 */
async function queueBatching(
  token: string,
  repository: string,
  queueRef: string,
  sha: string,
  head: string,
  fetcher: typeof fetch,
): Promise<string | undefined> {
  const base = queueEntry(queueRef)?.base;
  if (!base) {
    return `${queueRef} carries no base revision, so this entry's own change cannot be told from a batched one`;
  }
  // Neither comparison depends on the other's answer, and both are refused
  // together, so they are asked for together.
  const [queued, own] = await Promise.all([
    comparedFiles(token, repository, base, sha, fetcher),
    comparedFiles(token, repository, base, head, fetcher),
  ]);
  if (!queued || !own) {
    return "this queue entry could not be compared against the pull request's own change, so nothing rules out a batch";
  }
  if (queued.length !== own.length || queued.some((file, at) => file !== own[at])) {
    return "this queue entry batches more than one change, so one pull request's clearance does not speak for it";
  }
  return undefined;
}

// The platform's own cap on the files one comparison reports, and the page size
// asked for. A list at the cap is a list that may have been cut short, and two
// lists that may have been cut are not a comparison.
const COMPARE_FILE_LIMIT = 300;

/** As much of `GET /compare/:base...:head` as a batch is detected from. */
interface Comparison {
  files?: { filename?: string; sha?: string }[];
}

/**
 * The files one revision changes against another — each path with the blob that
 * path holds, one string per file, sorted — or nothing when the platform answered
 * no usable list.
 *
 * One string per file rather than a pair, because the two lists are compared and
 * never read apart: a single string is sorted into a canonical order by
 * `Array.prototype.sort`'s own code-unit ordering, with no comparator to get
 * wrong and no locale to make two distinct paths compare equal.
 *
 * Nothing rather than an empty list in the cases that matter: a revision this
 * repository does not have, a change too wide for the platform to list files for,
 * and an entry naming no path or no blob sha. An empty list is a real answer — a
 * comparison finding no changed file — and collapsing the two would read "nothing
 * to compare" as "the two agree". A file whose blob the platform did not name is
 * the same refusal for the same reason: a file that cannot be compared by content
 * is one a caller could otherwise match by path alone.
 */
async function comparedFiles(
  token: string,
  repository: string,
  base: string,
  head: string,
  fetcher: typeof fetch,
): Promise<string[] | undefined> {
  const response = await fetcher(
    `${GITHUB_API}/repos/${repository}/compare/${encodeURIComponent(base)}...${encodeURIComponent(
      head,
    )}?per_page=${COMPARE_FILE_LIMIT}`,
    { headers: apiHeaders(`Bearer ${token}`) },
  );
  if (response.status === 404) {
    return undefined;
  }
  if (!response.ok) {
    throw new Error(`comparing two revisions answered ${response.status}`);
  }
  const comparison = (await response.json()) as Comparison;
  if (!comparison.files || comparison.files.length >= COMPARE_FILE_LIMIT) {
    return undefined;
  }
  const files: string[] = [];
  for (const file of comparison.files) {
    if (!file.filename || !file.sha) {
      return undefined;
    }
    // NUL separates the two halves because it is the one byte a git path cannot
    // hold, so no path-and-blob pair encodes to the string another one does.
    files.push(`${file.filename}\0${file.sha}`);
  }
  return files.sort();
}

/**
 * The verdict a queue entry's submitted fingerprint earns against the clearance
 * standing on the originating pull request's head.
 *
 * Only a clearance `APP_STATUS_CREATOR` posted is read as one. A status any other
 * identity wrote under that context is an assertion by whoever held that token,
 * and it answers the same `pending` as a head carrying no clearance at all rather
 * than an error: a repository whose clearance was recorded some other way has a
 * referred entry to clear again, not a broken queue.
 *
 * `success` carries the clearer's own attribution forward rather than composing
 * one of lydite's: the decision the person judged is unchanged, so the judgement
 * is still theirs. Every other answer is `pending` and never `failure` — the
 * standing-referral state, and the one state `clearance.Decide` accepts a
 * clearing comment against. A `failure` here would make a mismatched entry
 * permanently unclearable, which is the deadlock ADR 0053 removes.
 */
function queueVerdict(
  cleared: StatusEntry | undefined,
  fingerprint: string,
  pull: number,
): Verdict {
  if (cleared?.state !== REFERRAL_RESOLVED) {
    return {
      state: REFERRAL_PENDING,
      description: clip(`no clearance stands on #${pull}'s head, so this entry stays referred`),
    };
  }
  if (cleared.creator?.login !== APP_STATUS_CREATOR) {
    return {
      state: REFERRAL_PENDING,
      description: clip(
        `#${pull}'s clearance was not posted by ${APP_STATUS_CREATOR}, so it cannot carry forward — clear #${pull} again`,
      ),
    };
  }
  const description = cleared.description ?? "";
  const recorded = fingerprintIn(description);
  if (!recorded) {
    return {
      state: REFERRAL_PENDING,
      description: clip(
        `#${pull}'s clearance records no decision fingerprint, so it cannot carry forward — clear #${pull} again`,
      ),
    };
  }
  if (recorded !== fingerprint) {
    return {
      state: REFERRAL_PENDING,
      description: clip(
        `the decision changed since #${pull} was cleared, so the clearance does not carry forward — clear #${pull} again`,
      ),
    };
  }
  return {
    state: REFERRAL_RESOLVED,
    description: clip(`${attributionIn(description)}, carried forward onto this queue entry`),
  };
}

/**
 * The fingerprint a status description carries, or nothing when it carries none
 * usable for a comparison.
 *
 * The reader of `internal/clearance`'s `WithFingerprint`, and it has to agree
 * with `FingerprintIn` in `source/cli/internal/clearance/decide.go` exactly:
 * nothing follows the fingerprint, so the last opening marker in a description
 * ending in the closing one is the field, and a value holding a bracket or a
 * space names nothing — which is what keeps brackets in the human half from
 * reading as a field.
 *
 * A description whose field is empty and one that has no field at all collapse
 * to the same answer here, unlike in Go where the second return distinguishes
 * them: both are unusable for a comparison, and this route's one use of either
 * is the same `pending`.
 */
function fingerprintIn(description: string): string | undefined {
  if (!description.endsWith(FINGERPRINT_CLOSE)) {
    return undefined;
  }
  const open = description.lastIndexOf(FINGERPRINT_OPEN);
  if (open < 0) {
    return undefined;
  }
  const value = description.slice(
    open + FINGERPRINT_OPEN.length,
    description.length - FINGERPRINT_CLOSE.length,
  );
  if (!value || /[ [\]]/.test(value)) {
    return undefined;
  }
  return value;
}

/**
 * The human half of a clearance's description: who cleared what, without the
 * fingerprint field.
 *
 * A description carrying no field is all human half, so nothing is cut from it.
 */
function attributionIn(description: string): string {
  if (!fingerprintIn(description)) {
    return description;
  }
  return description.slice(0, description.lastIndexOf(FINGERPRINT_OPEN));
}

/**
 * A description inside the platform's own cap.
 *
 * Composed here rather than left to the platform's clip, because what a clipped
 * description loses is its tail — which is where the reason a person has to act
 * on sits.
 */
function clip(description: string): string {
  const runes = [...description];
  if (runes.length <= DESCRIPTION_LIMIT) {
    return description;
  }
  return `${runes.slice(0, DESCRIPTION_LIMIT - 1).join("")}…`;
}

/**
 * Whether a `job_workflow_ref` is one of the exact strings a var lists.
 *
 * Commas and newlines both separate, because a Wrangler var is typed by hand
 * and a list of workflow refs is long enough to want breaking over lines.
 * Empty entries are dropped and an empty or unset var matches nothing — an
 * allowlist that is not configured admits no job rather than every job.
 */
function allowlisted(list: string | undefined, ref: string): boolean {
  if (!ref) {
    return false;
  }
  return (list ?? "")
    .split(/[,\n]/)
    .map((entry) => entry.trim())
    .filter(Boolean)
    .includes(ref);
}

/**
 * Why a submitted pull-request number may not be written to, or nothing.
 *
 * The repository is still the claim's, so what is in question is only whether
 * that repository has this pull request and whether it is open. Open, because
 * the run that submits a number is answering a comment somebody has just left
 * on a change under review; a closed pull request is not that conversation, and
 * admitting one would make every number the repository has ever issued writable
 * by a single job.
 */
async function submittedPullRefusal(
  token: string,
  repository: string,
  pull: number,
  fetcher: typeof fetch,
): Promise<string | undefined> {
  const pr = await pullRequest(token, repository, pull, fetcher);
  if (!pr) {
    return NO_SUCH_PULL;
  }
  if (pr.state !== "open") {
    return "the pull request submitted is not open";
  }
  return undefined;
}

const NO_SUCH_PULL = "this repository has no such pull request";

/** As much of `GET /pulls/:n` as any check here is decided on. */
interface PullRequest {
  state?: string;
  head?: { sha?: string };
}

/**
 * What GitHub says about a pull request, or nothing when the repository has
 * none by that number.
 *
 * A 404 is an answer about the number the caller named and is refused as one.
 * Every other failure is about lydite's own credentials or GitHub's
 * availability, so it throws and reaches the caller as the route's `502` —
 * which keeps a transport's three-way sort intact: a refusal is deterministic
 * and a `502` is worth retrying.
 */
async function pullRequest(
  token: string,
  repository: string,
  pull: number,
  fetcher: typeof fetch,
): Promise<PullRequest | undefined> {
  const response = await fetcher(`${GITHUB_API}/repos/${repository}/pulls/${pull}`, {
    headers: apiHeaders(`Bearer ${token}`),
  });
  if (response.status === 404) {
    return undefined;
  }
  if (!response.ok) {
    throw new Error(`resolving the pull request answered ${response.status}`);
  }
  return (await response.json()) as PullRequest;
}

/** One entry of `GET /commits/:sha/statuses`: what a context said, and who said it. */
interface StatusEntry {
  context?: string;
  state?: string;
  description?: string;
  /** Who posted it: the App's bot user for anything this relay wrote. */
  creator?: { login?: string };
}

// The page size asked of the statuses list. The newest post under a context is
// what a standing status is, so only the first page is read — a revision
// carrying more than this many statuses newer than the one wanted is a revision
// whose answer is not on the page, and the route's `pending` is what that reads
// as.
const STATUS_PAGE = 100;

/**
 * The status a context is currently standing at on a revision, or nothing when
 * that revision carries no such status.
 *
 * The list endpoint, not the combined one. `GET /commits/:sha/status`
 * deduplicates to one entry per context but reports no `creator` on any of them,
 * and a creator is the whole of what makes a status read back as lydite's own
 * authority rather than as whatever any `statuses: write` token asserted — a
 * field that is always `undefined` is a check that always fails, silently, on
 * every real request. The list answers every status ever posted, newest first,
 * so the first entry under the wanted context is that context's current state:
 * the same answer the combined endpoint composes, with the identity that answer
 * is only usable with.
 *
 * Read with the installation token and on the revision `handle` resolved,
 * because the point of the read is that it is the platform's answer rather than
 * the caller's: a request cannot assert the state it is about to be allowed to
 * move, nor the description a decision is compared against. Any failure other
 * than a missing revision throws and reaches the caller as the route's `502`, so
 * a transient outage stays retryable rather than reading as a refusal.
 */
async function standingStatus(
  token: string,
  repository: string,
  sha: string,
  context: string,
  fetcher: typeof fetch,
): Promise<StatusEntry | undefined> {
  const response = await fetcher(
    `${GITHUB_API}/repos/${repository}/commits/${encodeURIComponent(
      sha,
    )}/statuses?per_page=${STATUS_PAGE}`,
    { headers: apiHeaders(`Bearer ${token}`) },
  );
  if (response.status === 404) {
    return undefined;
  }
  if (!response.ok) {
    throw new Error(`reading the standing status answered ${response.status}`);
  }
  const posted = (await response.json()) as StatusEntry[];
  if (!Array.isArray(posted)) {
    return undefined;
  }
  return posted.find((status) => status.context === context);
}

/**
 * Records the verdict on the revision `handle` has already checked the caller's
 * `sha` against — the pull request's own head on `/status`, the queue revision
 * this run is for on `/merge-group` — and under a context it has already checked
 * too: the `lydite/`-namespaced one a caller named, or the referral context the
 * queue route composes its own verdict under. So by the time this runs, both the
 * revision and the repository are ones the run is already for, and the check is
 * one only lydite's own tooling could have named.
 */
async function postStatus(
  token: string,
  repository: string,
  status: StatusRequest,
  fetcher: typeof fetch,
): Promise<void> {
  const response = await fetcher(
    `${GITHUB_API}/repos/${repository}/statuses/${encodeURIComponent(status.sha as string)}`,
    {
      method: "POST",
      headers: { ...apiHeaders(`Bearer ${token}`), "content-type": "application/json" },
      body: JSON.stringify({
        state: status.state,
        context: status.context,
        description: status.description,
      }),
    },
  );
  if (!response.ok) {
    throw new Error(`writing the status answered ${response.status}`);
  }
}

function bearer(request: Request): string | undefined {
  const header = request.headers.get("authorization");
  if (!header?.toLowerCase().startsWith("bearer ")) {
    return undefined;
  }
  return header.slice("bearer ".length).trim() || undefined;
}

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}
