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

  // The pull request comes from `ref`, and a submitted number only has to
  // agree with it. A run whose ref is not a pull-request ref — a push build —
  // has no pull request to write to, and saying so is better than letting it
  // name one.
  const fromRef = pullRequestFromRef(claims.ref);
  if (!fromRef) {
    return json(403, { error: "this run is not for a pull request, so there is nothing to write to" });
  }

  const payload = (await request.json().catch(() => ({}))) as CommentRequest & ReviewOps & StatusRequest;
  if (payload.pull_request !== undefined && payload.pull_request !== fromRef) {
    return json(403, { error: "the pull request submitted is not the one this run is for" });
  }
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
      const outcome = await upsertComment(
        token,
        claims.repository,
        fromRef,
        payload.marker as string,
        payload.body as string,
        deps.fetcher,
      );
      return json(200, { repository: claims.repository, pull_request: fromRef, comment: outcome });
    }

    if (route === "/status") {
      // The pull request's head, not `claims.sha`: on a `pull_request` run
      // that claim is the platform's synthetic merge commit, which exists on
      // no branch and which no verdict is ever published against — the same
      // reason `forge.PullRequestEvent` reads a head from the event payload
      // rather than from `GITHUB_SHA`. Resolving it here, with the token
      // rather than trusting the body, is what lets `sha` be checked at all.
      const head = await pullRequestHeadSha(token, claims.repository, fromRef, deps.fetcher);
      if (payload.sha !== head) {
        return json(403, { error: "the sha submitted is not this pull request's head" });
      }
      await postStatus(token, claims.repository, payload, deps.fetcher);
      return json(200, {
        repository: claims.repository,
        pull_request: fromRef,
        status: "posted",
      });
    }

    // Every id the document names has to belong to this pull request, and
    // the whole request is refused if one does not. A comment id is a number
    // the caller supplies while `ref` is the one thing a run cannot choose,
    // so this is what stops a run deleting a comment on somebody else's pull
    // request — and refusing the whole document rather than the operation
    // means a caller cannot use the answer to probe which ids exist.
    const members = await reviewCommentIds(token, claims.repository, fromRef, deps.fetcher);
    const named = [
      ...(payload.reply ?? []).map((op) => op.comment),
      ...(payload.delete ?? []).map((op) => op.comment),
    ];
    if (named.some((id) => typeof id !== "number" || !members.has(id))) {
      return json(403, { error: "the operations name a comment that is not on this pull request" });
    }

    const outcomes = await applyReview(token, claims.repository, fromRef, payload, deps.fetcher);
    return json(200, { repository: claims.repository, pull_request: fromRef, outcomes });
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
 * The pull request's current head, so a submitted `sha` can be checked
 * against something GitHub itself says rather than the caller's own claim
 * about it.
 */
async function pullRequestHeadSha(
  token: string,
  repository: string,
  pull: number,
  fetcher: typeof fetch,
): Promise<string | undefined> {
  const response = await fetcher(`${GITHUB_API}/repos/${repository}/pulls/${pull}`, {
    headers: apiHeaders(`Bearer ${token}`),
  });
  if (!response.ok) {
    throw new Error(`resolving the pull request's head answered ${response.status}`);
  }
  const pr = (await response.json()) as { head?: { sha?: string } };
  return pr.head?.sha;
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
