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
   * Both default to empty, which admits no job at all — the state in which the
   * gated contexts are refused to every caller.
   */
  REFERRAL_WORKFLOW_REFS?: string;
  CLEARANCE_WORKFLOW_REFS?: string;
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

const JWKS = "https://token.actions.githubusercontent.com/.well-known/jwks";

const ROUTES = ["/comment", "/review", "/status"];

// The two contexts a job may only post from an allowlisted workflow: the
// referral verdict a merge is gated on, and the status a human Clearance acts
// on. They are distinct names so that one workflow's authority to post its own
// is never authority to post the other.
const REFERRAL_CONTEXT = "lydite/referral";
const CLEARANCE_CONTEXT = "lydite/clearance";

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
 *
 * Three endpoints, and each writes to one pull request and nothing else.
 * `POST /comment` upserts the standing comment; `POST /review` applies the
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
 * That covers those three writes and nothing else. The coverage gate runs the
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
    return json(404, { error: "POST /comment, POST /review or POST /status" });
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

  const payload = (await request.json().catch(() => ({}))) as CommentRequest & ReviewOps & StatusRequest;

  const target = resolveTarget(route, claims, payload, env);
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
    const refused = statusAuthorityError(payload.context as string, claims.job_workflow_ref, env);
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
      if (payload.sha !== pr.head?.sha) {
        return json(403, { error: "the sha submitted is not this pull request's head" });
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
 * That exemption is keyed on the verified `job_workflow_ref` being one the
 * clearance allowlist names, and on nothing in the body — a request cannot ask
 * for it. Every other run, an allowlisted referral workflow included, keeps
 * deriving the pull request from its ref and is refused when that ref names
 * none. `/review` is outside the exemption too: a clearance run has no
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
  if (route !== "/review" && clearanceRun(claims.job_workflow_ref, env)) {
    const named = payload.pull_request;
    if (typeof named !== "number" || !Number.isInteger(named) || named <= 0) {
      return { error: "this run's ref names no pull request, so a pull request number is required" };
    }
    return { pull: named, submitted: true };
  }
  return { error: "this run is not for a pull request, so there is nothing to write to" };
}

/**
 * Whether this run is a clearance run: one whose `job_workflow_ref` the
 * clearance allowlist names and the referral allowlist does not.
 *
 * A ref in both lists holds neither authority. `statusAuthorityError` refuses it
 * outright as the deployment mistake it is, and reading it as a clearance run
 * here would hand a referral workflow — which runs beside the pull request's own
 * code — the one thing that separates the two.
 */
function clearanceRun(ref: string | undefined, env: Env): boolean {
  const named = ref ?? "";
  return (
    allowlisted(env.CLEARANCE_WORKFLOW_REFS, named) && !allowlisted(env.REFERRAL_WORKFLOW_REFS, named)
  );
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
 * The pairing is exclusive in both directions. An allowlisted referral workflow
 * may post the referral context and nothing else, and a clearance workflow the
 * clearance context and nothing else, so that being trusted to author one
 * verdict is never being trusted to author the other. A job whose ref is in
 * neither list is unaffected outside the gated contexts: the `lydite/` namespace
 * check is the whole of what governs the rest.
 */
function statusAuthorityError(
  context: string,
  ref: string | undefined,
  env: Env,
): string | undefined {
  const named = ref ?? "";
  const referral = allowlisted(env.REFERRAL_WORKFLOW_REFS, named);
  const clearance = allowlisted(env.CLEARANCE_WORKFLOW_REFS, named);
  if (referral && clearance) {
    // One ref cannot hold both authorities, and resolving the ambiguity toward
    // either would grant an authority nobody wrote down. It is a deployment
    // mistake, refused rather than interpreted.
    return `${named} is allowlisted for both ${REFERRAL_CONTEXT} and ${CLEARANCE_CONTEXT}`;
  }
  const permitted = referral ? REFERRAL_CONTEXT : clearance ? CLEARANCE_CONTEXT : undefined;
  if (permitted) {
    return context === permitted ? undefined : `${named} may post ${permitted} only`;
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
    .filter((entry) => entry.length > 0)
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

/**
 * Records the verdict on the revision `handle` has already resolved and
 * checked the caller's `sha` against, and under the `lydite/`-namespaced
 * `context` it has already checked too — so by the time this runs, both the
 * revision and the repository are ones the run is already for, and the check
 * is one only lydite's own tooling could have named.
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
