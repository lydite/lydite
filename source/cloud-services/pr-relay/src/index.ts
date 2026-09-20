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
  if (
    route === "/status" &&
    (!payload.state || !payload.context || !payload.description || !payload.sha)
  ) {
    return json(400, { error: "a state, a context, a description and a sha are required" });
  }
  if (route === "/status" && payload.sha !== claims.sha) {
    // The claim, not the body, says which revision this run is for — the same
    // reason `pull_request` is checked against `ref` above. Without this, a
    // sha is just a string the caller wrote, and the App's identity would
    // sign a status onto any commit of the repository the run is already for.
    return json(403, { error: "the sha submitted is not the one this run is for" });
  }
  if (route === "/status" && !payload.context?.startsWith("lydite/")) {
    // A context is a check's name, and this is the App's identity to spend.
    // Confined to lydite's own namespace, the worst a caller can do is author
    // a wrong lydite check — never stand in for a check that belongs to
    // another tool.
    return json(400, { error: "a status context must start with lydite/" });
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
 * Records the verdict on the revision the run's own claim named.
 *
 * `handle` has already checked that the caller's `sha` is `claims.sha` and
 * that its `context` is lydite's own, so by the time this runs both the
 * revision and the repository are the claim's, not the caller's — the only
 * status this can ever write is one the run is already for.
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
