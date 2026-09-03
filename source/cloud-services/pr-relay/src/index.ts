import {
  appJwt,
  installationId,
  installationToken,
  pullRequestFromRef,
  upsertComment,
  verifyActionsToken,
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

const JWKS = "https://token.actions.githubusercontent.com/.well-known/jwks";

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
 * It exists so that a CI job can have lydite comment on a pull request without
 * holding a credential that could write anywhere. The job presents the OIDC
 * token GitHub minted for that run; the relay checks it, decides from the
 * verified claims which repository and which pull request may be written to,
 * mints an installation token narrowed to exactly that, posts, and discards it.
 *
 * That covers the comment and nothing else. The coverage gate runs the
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
    if (request.method !== "POST" || new URL(request.url).pathname !== "/comment") {
      return json(404, { error: "POST /comment" });
    }

    const presented = bearer(request);
    if (!presented) {
      return json(401, { error: "no Actions OIDC token was presented" });
    }

    let claims;
    try {
      claims = await verifyActionsToken(presented, env.AUDIENCE, deps.fetchJwks);
    } catch {
      // No detail. A verifier that says which check failed tells a caller how
      // to get closer, one attempt at a time.
      return json(401, { error: "the Actions OIDC token did not verify" });
    }

    const payload = (await request.json().catch(() => ({}))) as CommentRequest;
    if (!payload.body || !payload.marker) {
      return json(400, { error: "a marker and a body are required" });
    }

    // The pull request comes from `ref`, and the submitted number only has to
    // agree with it. A run whose ref is not a pull-request ref — a push build —
    // has no pull request to comment on, and saying so is better than letting
    // it name one.
    const fromRef = pullRequestFromRef(claims.ref);
    if (!fromRef) {
      return json(403, { error: "this run is not for a pull request, so there is nothing to comment on" });
    }
    if (payload.pull_request !== undefined && payload.pull_request !== fromRef) {
      return json(403, { error: "the pull request submitted is not the one this run is for" });
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
      const outcome = await upsertComment(
        token,
        claims.repository,
        fromRef,
        payload.marker,
        payload.body,
        deps.fetcher,
      );
      return json(200, { repository: claims.repository, pull_request: fromRef, comment: outcome });
    } catch {
      // The reason is deliberately not relayed. It is about lydite's own
      // credentials and GitHub's answers to them, neither of which is the
      // caller's to see.
      return json(502, { error: "the comment could not be posted" });
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
