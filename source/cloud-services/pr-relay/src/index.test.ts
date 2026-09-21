import { beforeAll, describe, expect, it } from "vitest";

import { ACTIONS_ISSUER } from "@lydite/github-app";
import { issuerKeys } from "@lydite/github-app/testing";

import { createRelay, type Env } from "./index.js";

const AUDIENCE = "https://pr.lydite.org";

let keys: Awaited<ReturnType<typeof issuerKeys>>;
let env: Env;

beforeAll(async () => {
  keys = await issuerKeys();
  env = {
    LYDITE_APP_ID: "1234",
    LYDITE_APP_PRIVATE_KEY: await keys.privateKeyPkcs8Pem(),
    AUDIENCE,
  };
});

function claims(overrides: Record<string, unknown> = {}) {
  return {
    iss: ACTIONS_ISSUER,
    aud: AUDIENCE,
    exp: Math.floor(Date.now() / 1000) + 300,
    repository: "lydite/proving-ground",
    ref: "refs/pull/7/merge",
    sha: "abc123",
    ...overrides,
  };
}

// Every GitHub call the relay would make, answered locally. A test that let
// them reach the network would be testing GitHub.
function githubStub(): typeof fetch {
  return (async (url: unknown, init?: RequestInit) => {
    const target = String(url);
    if (target.endsWith("/installation")) {
      return Response.json({ id: 42 });
    }
    if (target.includes("/access_tokens")) {
      return Response.json({ token: "ghs_test" });
    }
    if (target.includes("/comments?")) {
      return Response.json([]);
    }
    if (target.includes("/comments")) {
      expect(init?.method).toBe("POST");
      return Response.json({ id: 1 });
    }
    throw new Error(`the relay called something unexpected: ${target}`);
  }) as typeof fetch;
}

function post(
  token: string | undefined,
  body: unknown,
  fetcher: typeof fetch = githubStub(),
): Promise<Response> {
  const relay = createRelay({ fetchJwks: keys.jwks, fetcher });
  return relay.fetch(
    new Request("https://pr.lydite.org/comment", {
      method: "POST",
      headers: token ? { authorization: `Bearer ${token}` } : {},
      body: JSON.stringify(body),
    }),
    env,
  );
}

const comment = { marker: "<!-- lydite:results -->", body: "a verdict" };

describe("the relay's trust boundary", () => {
  it("refuses a request presenting no token", async () => {
    expect((await post(undefined, comment)).status).toBe(401);
  });

  it("refuses an expired token", async () => {
    const token = await keys.sign(claims({ exp: Math.floor(Date.now() / 1000) - 1 }));
    expect((await post(token, comment)).status).toBe(401);
  });

  it("refuses a token minted for another audience", async () => {
    const token = await keys.sign(claims({ aud: "https://elsewhere" }));
    expect((await post(token, comment)).status).toBe(401);
  });

  // The repository is taken from the claim and never from the body, so there is
  // nothing in a request that could name a different one. This is what stops a
  // job commenting on somebody else's repository.
  it("takes the repository from the claim, so the body cannot name another", async () => {
    const called: string[] = [];
    const watching: typeof fetch = (async (url: unknown, init?: RequestInit) => {
      called.push(String(url));
      return githubStub()(url as string, init);
    }) as typeof fetch;

    const token = await keys.sign(claims());
    const response = await post(token, { ...comment, repository: "someone/else" }, watching);

    expect(response.status).toBe(200);
    await expect(response.json()).resolves.toMatchObject({
      repository: "lydite/proving-ground",
      pull_request: 7,
    });
    for (const url of called) {
      expect(url).not.toContain("someone/else");
    }
  });

  // The whole point: a verified request posts, with a token minted for one
  // repository and one permission and then discarded.
  it("posts the comment for a verified run", async () => {
    const token = await keys.sign(claims());
    const response = await post(token, comment);
    expect(response.status).toBe(200);
    await expect(response.json()).resolves.toMatchObject({ comment: "created" });
  });

  // An answer rather than a failure: the consumer has not opted in, and the
  // client's next move is its own token. Reporting it as an error is what
  // would make the fallback look like something going wrong.
  it("says the app is not installed, and names the fallback", async () => {
    const notInstalled: typeof fetch = (async (url: unknown) => {
      if (String(url).endsWith("/installation")) {
        return new Response("", { status: 404 });
      }
      throw new Error("nothing else should be called");
    }) as typeof fetch;

    const token = await keys.sign(claims());
    const response = await post(token, comment, notInstalled);
    expect(response.status).toBe(409);
    await expect(response.json()).resolves.toMatchObject({
      fallback: "post with the workflow's own token",
    });
  });

  // A run that is not a pull request's has nothing to comment on, and the ref
  // is the only statement of that the run cannot choose.
  it("refuses a run whose ref is not a pull request's", async () => {
    const token = await keys.sign(claims({ ref: "refs/heads/main" }));
    expect((await post(token, comment)).status).toBe(403);
  });

  it("refuses a pull-request number that disagrees with the ref", async () => {
    const token = await keys.sign(claims({ ref: "refs/pull/7/merge" }));
    const response = await post(token, { ...comment, pull_request: 9 });
    expect(response.status).toBe(403);
  });

  it("requires a marker and a body", async () => {
    const token = await keys.sign(claims());
    expect((await post(token, {})).status).toBe(400);
  });

  it("answers only the endpoints it has", async () => {
    const relay = createRelay({ fetchJwks: keys.jwks, fetcher: githubStub() });
    const response = await relay.fetch(new Request("https://pr.lydite.org/"), env);
    expect(response.status).toBe(404);
  });

  // A rejection that explains which check failed is an oracle: it tells a
  // caller how to get one step closer, one attempt at a time.
  it("says nothing about which check a rejected token failed", async () => {
    const token = await keys.sign(claims({ aud: "https://elsewhere" }));
    const body = JSON.stringify(await (await post(token, comment)).json());
    for (const leak of ["audience", "aud", "exp", "issuer", "signature", "kid"]) {
      expect(body.toLowerCase()).not.toContain(leak);
    }
  });
});

// Every review call the relay would make, answered locally. `existing` is the
// pull request's own review comments, which is the set an operation's id has
// to be in.
function reviewStub(existing: number[] = [5]): typeof fetch {
  return (async (url: unknown, init?: RequestInit) => {
    const target = String(url);
    if (target.endsWith("/installation")) {
      return Response.json({ id: 42 });
    }
    if (target.includes("/access_tokens")) {
      return Response.json({ token: "ghs_test" });
    }
    if (target.includes("/pulls/7/comments?")) {
      return Response.json(existing.map((id) => ({ id })));
    }
    if (target.endsWith("/pulls/7/reviews") || target.includes("/replies")) {
      return Response.json({ id: 1 });
    }
    if (init?.method === "DELETE") {
      return new Response(null, { status: 204 });
    }
    throw new Error(`the relay called something unexpected: ${target}`);
  }) as typeof fetch;
}

function postReview(
  token: string | undefined,
  body: unknown,
  fetcher: typeof fetch = reviewStub(),
): Promise<Response> {
  const relay = createRelay({ fetchJwks: keys.jwks, fetcher });
  return relay.fetch(
    new Request("https://pr.lydite.org/review", {
      method: "POST",
      headers: token ? { authorization: `Bearer ${token}` } : {},
      body: JSON.stringify(body),
    }),
    env,
  );
}

describe("applying a review", () => {
  it("answers per operation, so a posted review and a refused delete are told apart", async () => {
    const token = await keys.sign(claims());
    const response = await postReview(token, {
      version: 1,
      head: "abc",
      create: [{ path: "a.go", line: 3, subject: "line", body: "a claim" }],
      delete: [{ comment: 5, refused: "cleared" }],
    });
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({
      repository: "lydite/proving-ground",
      pull_request: 7,
      outcomes: [
        { op: "delete", ref: 5, status: "done" },
        { op: "create", ref: 1, status: "done" },
      ],
    });
  });

  // A comment id is a number the caller supplies, and `ref` is the only thing
  // a run cannot choose. Refusing the whole document rather than the one
  // operation is what stops the answer being used to probe which ids exist.
  it("refuses the whole document when an id is not on this pull request", async () => {
    const token = await keys.sign(claims());
    const response = await postReview(token, {
      version: 1,
      delete: [{ comment: 5 }, { comment: 999 }],
      create: [{ path: "a.go", line: 3, subject: "line", body: "x" }],
    });
    expect(response.status).toBe(403);
  });

  it("refuses a document version it does not apply", async () => {
    const token = await keys.sign(claims());
    expect((await postReview(token, { version: 99, create: [] })).status).toBe(400);
    expect((await postReview(token, { create: [] })).status).toBe(400);
  });

  it("takes the pull request from the ref and not from the document", async () => {
    const token = await keys.sign(claims({ ref: "refs/pull/7/merge" }));
    expect((await postReview(token, { version: 1, pull_request: 9 })).status).toBe(403);
  });

  it("refuses a run that is not for a pull request", async () => {
    const token = await keys.sign(claims({ ref: "refs/heads/main" }));
    expect((await postReview(token, { version: 1 })).status).toBe(403);
  });

  it("refuses a request presenting no token", async () => {
    expect((await postReview(undefined, { version: 1 })).status).toBe(401);
  });

  // Not installed is an answer, so the client falls back to its own token
  // rather than reporting a failure — the same designed path the comment has.
  it("says the app is not installed rather than failing", async () => {
    const token = await keys.sign(claims());
    const notInstalled = (async (url: unknown) =>
      String(url).endsWith("/installation")
        ? new Response("no", { status: 404 })
        : Response.json({})) as typeof fetch;
    const response = await postReview(token, { version: 1 }, notInstalled);
    expect(response.status).toBe(409);
    expect(await response.json()).toMatchObject({ fallback: expect.any(String) });
  });

  it("says nothing about lydite's own credentials when a call fails", async () => {
    const token = await keys.sign(claims());
    const broken = (async (url: unknown) => {
      const target = String(url);
      if (target.endsWith("/installation")) return Response.json({ id: 42 });
      if (target.includes("/access_tokens")) return Response.json({ token: "ghs_test" });
      return new Response("upstream detail", { status: 500 });
    }) as typeof fetch;
    const response = await postReview(token, { version: 1, create: [] }, broken);
    expect(response.status).toBe(502);
    const body = JSON.stringify(await response.json());
    expect(body).not.toContain("upstream detail");
    // Each endpoint says which of its own writes did not happen, so a reader
    // of the job log is not told the comment failed when the review did.
    expect(body).toContain("review");
    expect(body).not.toContain("comment");
  });
});

// Every status call the relay would make, answered locally. `written` collects
// what reached the statuses endpoint, which is the only write this route makes.
// `headSha` is what `GET /pulls/:n` answers with — the pull request's real
// head, distinct in principle from the OIDC claim's own `sha`.
function statusStub(
  written: { url: string; init?: RequestInit }[] = [],
  headSha = "abc123",
): typeof fetch {
  return (async (url: unknown, init?: RequestInit) => {
    const target = String(url);
    if (target.endsWith("/installation")) {
      return Response.json({ id: 42 });
    }
    if (target.includes("/access_tokens")) {
      return Response.json({ token: "ghs_test" });
    }
    if (target.endsWith("/pulls/7")) {
      return Response.json({ head: { sha: headSha } });
    }
    if (target.includes("/statuses/")) {
      written.push({ url: target, init });
      return Response.json({ id: 1 });
    }
    throw new Error(`the relay called something unexpected: ${target}`);
  }) as typeof fetch;
}

function postStatus(
  token: string | undefined,
  body: unknown,
  fetcher: typeof fetch = statusStub(),
): Promise<Response> {
  const relay = createRelay({ fetchJwks: keys.jwks, fetcher });
  return relay.fetch(
    new Request("https://pr.lydite.org/status", {
      method: "POST",
      headers: token ? { authorization: `Bearer ${token}` } : {},
      body: JSON.stringify(body),
    }),
    env,
  );
}

const verdict = {
  state: "pending",
  context: "lydite/some-check",
  description: "waiting on a reviewer",
  sha: "abc123",
};

describe("recording a status", () => {
  // The whole point of the endpoint: the status is authored by the App's own
  // installation token, on the revision the client named.
  it("posts the status with the app's token", async () => {
    const written: { url: string; init?: RequestInit }[] = [];
    const token = await keys.sign(claims());
    const response = await postStatus(token, verdict, statusStub(written));

    expect(response.status).toBe(200);
    expect(written).toHaveLength(1);
    expect(written[0]?.url).toBe(
      "https://api.github.com/repos/lydite/proving-ground/statuses/abc123",
    );
    expect(written[0]?.init?.method).toBe("POST");
    expect((written[0]?.init?.headers as Record<string, string> | undefined)?.authorization).toBe(
      "Bearer ghs_test",
    );
    expect(JSON.parse(String(written[0]?.init?.body))).toEqual({
      state: "pending",
      context: "lydite/some-check",
      description: "waiting on a reviewer",
    });
  });

  // The repository is the claim's, so a body naming another one changes
  // nothing about where the status lands.
  it("takes the repository from the claim, so the body cannot name another", async () => {
    const written: { url: string; init?: RequestInit }[] = [];
    const token = await keys.sign(claims());
    const response = await postStatus(
      token,
      { ...verdict, repository: "someone/else" },
      statusStub(written),
    );

    expect(response.status).toBe(200);
    await expect(response.json()).resolves.toMatchObject({
      repository: "lydite/proving-ground",
    });
    expect(written).toHaveLength(1);
    expect(written[0]?.url).not.toContain("someone/else");
  });

  // Nothing is defaulted. A state, a context or a description the caller did
  // not send is a status saying something nobody wrote.
  it("requires a state, a context, a description and a sha", async () => {
    const token = await keys.sign(claims());
    expect((await postStatus(token, {})).status).toBe(400);
    for (const field of ["state", "context", "description", "sha"]) {
      const partial: Record<string, unknown> = { ...verdict };
      delete partial[field];
      expect((await postStatus(token, partial)).status).toBe(400);
    }
  });

  // The revision is the pull request's own head, resolved from GitHub rather
  // than trusted from the body: naming any other commit must not sign a
  // status onto it with the App's identity.
  it("refuses a sha that is not the pull request's head", async () => {
    const token = await keys.sign(claims());
    const response = await postStatus(
      token,
      { ...verdict, sha: "someone-elses-sha" },
      statusStub([], "abc123"),
    );
    expect(response.status).toBe(403);
  });

  // On a pull_request run the OIDC claim's own `sha` is the platform's
  // synthetic merge commit — a revision that exists on no branch and that no
  // verdict is ever published against. A caller submitting the pull
  // request's real head must still succeed even though it disagrees with
  // `claims.sha`, which is exactly what a legitimate call looks like.
  it("accepts the pull request's head even when it differs from the claim's own sha", async () => {
    const written: { url: string; init?: RequestInit }[] = [];
    const token = await keys.sign(claims({ sha: "merge-commit-sha" }));
    const response = await postStatus(
      token,
      { ...verdict, sha: "pr-head-sha" },
      statusStub(written, "pr-head-sha"),
    );

    expect(response.status).toBe(200);
    expect(written).toHaveLength(1);
    expect(written[0]?.url).toContain("/statuses/pr-head-sha");
  });

  // A context is a check's name, and this is the App's identity to spend: it
  // must not be asked to stand in for a check belonging to another tool.
  it("refuses a context outside lydite's own namespace", async () => {
    const token = await keys.sign(claims());
    const response = await postStatus(token, { ...verdict, context: "some-other-tool/check" });
    expect(response.status).toBe(400);
  });

  // The gated contexts are admitted only from a `job_workflow_ref` the
  // allowlist names, and an empty allowlist names none — so the verdict a
  // merge is gated on is refused to every caller, `lydite/` prefix or not.
  it("refuses a gated context to a job no allowlist names", async () => {
    const token = await keys.sign(claims());
    const response = await postStatus(token, { ...verdict, context: "lydite/referral" });
    expect(response.status).toBe(403);
  });

  // The same designed path the comment has: not installed is an answer, and
  // the client's next move is its own token.
  it("says the app is not installed rather than failing", async () => {
    const notInstalled = (async (url: unknown) =>
      String(url).endsWith("/installation")
        ? new Response("no", { status: 404 })
        : Response.json({})) as typeof fetch;

    const token = await keys.sign(claims());
    const response = await postStatus(token, verdict, notInstalled);
    expect(response.status).toBe(409);
    await expect(response.json()).resolves.toMatchObject({
      fallback: "post with the workflow's own token",
    });
  });

  it("refuses a request presenting no token", async () => {
    expect((await postStatus(undefined, verdict)).status).toBe(401);
  });

  it("refuses a token minted for another audience", async () => {
    const token = await keys.sign(claims({ aud: "https://elsewhere" }));
    expect((await postStatus(token, verdict)).status).toBe(401);
  });

  it("refuses an expired token", async () => {
    const token = await keys.sign(claims({ exp: Math.floor(Date.now() / 1000) - 1 }));
    expect((await postStatus(token, verdict)).status).toBe(401);
  });

  it("refuses a run that is not for a pull request", async () => {
    const token = await keys.sign(claims({ ref: "refs/heads/main" }));
    expect((await postStatus(token, verdict)).status).toBe(403);
  });

  it("says nothing about lydite's own credentials when the write fails", async () => {
    const broken = (async (url: unknown) => {
      const target = String(url);
      if (target.endsWith("/installation")) return Response.json({ id: 42 });
      if (target.includes("/access_tokens")) return Response.json({ token: "ghs_test" });
      return new Response("upstream detail", { status: 500 });
    }) as typeof fetch;

    const token = await keys.sign(claims());
    const response = await postStatus(token, verdict, broken);
    expect(response.status).toBe(502);
    const body = JSON.stringify(await response.json());
    expect(body).not.toContain("upstream detail");
    expect(body).toContain("status");
  });
});

const REFERRAL_REF =
  "lydite/actions/.github/workflows/referral.yml@refs/tags/v1";
const CLEARANCE_REF =
  "lydite/actions/.github/workflows/clearance.yml@refs/tags/v1";
function gatedEnv(overrides: Partial<Env> = {}): Env {
  return {
    ...env,
    REFERRAL_WORKFLOW_REFS: REFERRAL_REF,
    CLEARANCE_WORKFLOW_REFS: CLEARANCE_REF,
    ...overrides,
  };
}

// Answers `GET /pulls/:n` for every number in `pulls` (a missing number is a
// 404) and records each status and comment write.
function pullStub(
  pulls: Record<number, { state: string; sha: string }>,
  written: { url: string; init?: RequestInit }[] = [],
): typeof fetch {
  return (async (url: unknown, init?: RequestInit) => {
    const target = String(url);
    if (target.endsWith("/installation")) {
      return Response.json({ id: 42 });
    }
    if (target.includes("/access_tokens")) {
      return Response.json({ token: "ghs_test" });
    }
    const pull = /\/pulls\/(\d+)$/.exec(target);
    if (pull) {
      const found = pulls[Number(pull[1])];
      return found
        ? Response.json({ state: found.state, head: { sha: found.sha } })
        : new Response("", { status: 404 });
    }
    if (target.includes("/comments?")) {
      return Response.json([]);
    }
    if (target.includes("/statuses/") || target.includes("/comments")) {
      written.push({ url: target, init });
      return Response.json({ id: 1 });
    }
    throw new Error(`the relay called something unexpected: ${target}`);
  }) as typeof fetch;
}

function send(
  route: string,
  token: string,
  body: unknown,
  relayEnv: Env,
  fetcher: typeof fetch,
): Promise<Response> {
  const relay = createRelay({ fetchJwks: keys.jwks, fetcher });
  return relay.fetch(
    new Request(`https://pr.lydite.org${route}`, {
      method: "POST",
      headers: { authorization: `Bearer ${token}` },
      body: JSON.stringify(body),
    }),
    relayEnv,
  );
}

const open7 = { 7: { state: "open", sha: "abc123" } };

describe("gating the referral and clearance contexts on job_workflow_ref", () => {
  const referral = { ...verdict, context: "lydite/referral" };
  const clearance = { ...verdict, context: "lydite/clearance" };
  // What a clearance run presents: `lydite-clearance.yml` runs on
  // `issue_comment` from the default branch, so the token is minted for a
  // branch ref.
  const clearanceClaims = { ref: "refs/heads/main", event_name: "issue_comment" };

  async function status(
    ref: string | undefined,
    body: unknown,
    relayEnv = gatedEnv(),
    claim = {},
  ) {
    const written: { url: string; init?: RequestInit }[] = [];
    const token = await keys.sign(claims({ job_workflow_ref: ref, ...claim }));
    const response = await send(
      "/status",
      token,
      body,
      relayEnv,
      pullStub(open7, written),
    );
    return { response, written };
  }

  it("admits an allowlisted referral workflow to the referral context", async () => {
    const { response, written } = await status(REFERRAL_REF, referral);
    expect(response.status).toBe(200);
    expect(written).toHaveLength(1);
  });

  it("admits an allowlisted clearance workflow to the clearance context", async () => {
    const { response, written } = await status(
      CLEARANCE_REF,
      { ...clearance, pull_request: 7 },
      gatedEnv(),
      clearanceClaims,
    );
    expect(response.status).toBe(200);
    expect(written).toHaveLength(1);
  });

  it("refuses an unlisted workflow, naming its ref", async () => {
    const ref = "someone/else/.github/workflows/x.yml@refs/heads/main";
    const { response, written } = await status(ref, referral);
    expect(response.status).toBe(403);
    expect(JSON.stringify(await response.json())).toContain(ref);
    expect(written).toHaveLength(0);
  });

  it("refuses a same-repository reusable workflow on the pull request's own ref", async () => {
    const ref =
      "lydite/proving-ground/.github/workflows/x.yml@refs/pull/7/merge";
    expect((await status(ref, referral)).response.status).toBe(403);
    expect((await status(ref, clearance)).response.status).toBe(403);
  });

  it("matches the allowlisted string exactly", async () => {
    const near = [
      REFERRAL_REF.slice(0, -3),
      REFERRAL_REF.replace("@refs/tags/v1", "@refs/heads/main"),
      `${REFERRAL_REF}x`,
      REFERRAL_REF.replace("referral.yml", "referral.yml.evil"),
      REFERRAL_REF.replace(/@.*/, ""),
    ];
    for (const ref of near) {
      expect((await status(ref, referral)).response.status).toBe(403);
    }
  });

  it("keeps each workflow to its own context", async () => {
    expect((await status(REFERRAL_REF, clearance)).response.status).toBe(403);
    expect(
      (
        await status(
          CLEARANCE_REF,
          { ...referral, pull_request: 7 },
          gatedEnv(),
          clearanceClaims,
        )
      ).response.status,
    ).toBe(403);
  });

  it("refuses a ref allowlisted for both contexts", async () => {
    const both = gatedEnv({
      REFERRAL_WORKFLOW_REFS: REFERRAL_REF,
      CLEARANCE_WORKFLOW_REFS: REFERRAL_REF,
    });
    expect((await status(REFERRAL_REF, referral, both)).response.status).toBe(
      403,
    );
    expect((await status(REFERRAL_REF, clearance, both)).response.status).toBe(
      403,
    );
  });

  it("refuses the gated contexts to every caller when the allowlists are empty", async () => {
    for (const relayEnv of [
      gatedEnv({ REFERRAL_WORKFLOW_REFS: "", CLEARANCE_WORKFLOW_REFS: "" }),
      env,
    ]) {
      expect(
        (await status(REFERRAL_REF, referral, relayEnv)).response.status,
      ).toBe(403);
      expect(
        (await status(CLEARANCE_REF, clearance, relayEnv)).response.status,
      ).toBe(403);
      expect(
        (await status(undefined, referral, relayEnv)).response.status,
      ).toBe(403);
      expect(
        (await status(REFERRAL_REF, verdict, relayEnv)).response.status,
      ).toBe(200);
    }
  });

  it("reads an allowlist separated by commas and newlines, padded and with empty entries", async () => {
    const relayEnv = gatedEnv({
      REFERRAL_WORKFLOW_REFS: `  other/x.yml@a ,, \n  ${REFERRAL_REF}  \n\n,`,
    });
    expect(
      (await status(REFERRAL_REF, referral, relayEnv)).response.status,
    ).toBe(200);
    expect(
      (await status("other/x.yml@a", referral, relayEnv)).response.status,
    ).toBe(200);
    expect((await status("", referral, relayEnv)).response.status).toBe(403);
  });

  it("names a missing job_workflow_ref as such", async () => {
    const { response } = await status(undefined, referral);
    expect(response.status).toBe(403);
    expect(JSON.stringify(await response.json())).toContain(
      "names no job_workflow_ref",
    );
  });

  describe("a clearance run's pull request", () => {
    const named = { ...clearance, pull_request: 7 };

    it("is resolved live for a status, and bound on the head sha", async () => {
      const ok = await status(CLEARANCE_REF, named, gatedEnv(), clearanceClaims);
      expect(ok.response.status).toBe(200);
      expect(ok.written).toHaveLength(1);

      const stale = await status(
        CLEARANCE_REF,
        { ...named, sha: "stale" },
        gatedEnv(),
        clearanceClaims,
      );
      expect(stale.response.status).toBe(403);
      expect(stale.written).toHaveLength(0);

      const missing = await status(
        CLEARANCE_REF,
        { ...named, pull_request: 8 },
        gatedEnv(),
        clearanceClaims,
      );
      expect(missing.response.status).toBe(403);
      expect(missing.written).toHaveLength(0);
    });

    it("is not offered to a referral workflow, on a status", async () => {
      const { response } = await status(
        REFERRAL_REF,
        { ...referral, pull_request: 7 },
        gatedEnv(),
        clearanceClaims,
      );
      expect(response.status).toBe(403);
    });

    it("is not offered to an unlisted workflow", async () => {
      const { response } = await status(
        "x/y/.github/workflows/z.yml@main",
        { ...verdict, pull_request: 7 },
        gatedEnv(),
        clearanceClaims,
      );
      expect(response.status).toBe(403);
    });

    it("is not offered to a workflow allowlisted for both", async () => {
      const both = gatedEnv({ REFERRAL_WORKFLOW_REFS: CLEARANCE_REF });
      const { response } = await status(CLEARANCE_REF, named, both, clearanceClaims);
      expect(response.status).toBe(403);
    });

    // The allowlist names a workflow file, and the same callee is reachable
    // from a caller running on any event. Naming a pull request in the body,
    // and posting the clearance status, belong to the one event the clearance
    // workflow runs on.
    it("is not offered to a run on another event", async () => {
      const impostors = [
        { ref: "refs/heads/main", event_name: "push" },
        { ref: "refs/heads/main", event_name: "workflow_dispatch" },
        { ref: "refs/pull/7/merge", event_name: "pull_request" },
      ];
      for (const claim of impostors) {
        const posted = await status(CLEARANCE_REF, named, gatedEnv(), claim);
        expect(posted.response.status).toBe(403);
        expect(posted.written).toHaveLength(0);
        expect(JSON.stringify(await posted.response.json())).toContain(
          claim.event_name,
        );

        const commented = await commentOn(
          open7,
          { ...comment, pull_request: 9 },
          CLEARANCE_REF,
          claim,
        );
        expect(commented.response.status).toBe(403);
        expect(commented.written).toHaveLength(0);
      }
    });

    it("is not offered to a run naming no event", async () => {
      const { response, written } = await status(CLEARANCE_REF, named, gatedEnv(), {
        ref: "refs/heads/main",
      });
      expect(response.status).toBe(403);
      expect(written).toHaveLength(0);
      expect(JSON.stringify(await response.json())).toContain(
        "an unnamed event",
      );
    });

    it("never overrides the pull request a pull ref names", async () => {
      const { response } = await status(REFERRAL_REF, {
        ...referral,
        pull_request: 9,
      });
      expect(response.status).toBe(403);
    });

    async function commentOn(
      pulls: Record<number, { state: string; sha: string }>,
      body: unknown,
      ref = CLEARANCE_REF,
      claim: object = clearanceClaims,
    ) {
      const written: { url: string; init?: RequestInit }[] = [];
      const token = await keys.sign(
        claims({ job_workflow_ref: ref, ...claim }),
      );
      const response = await send(
        "/comment",
        token,
        body,
        gatedEnv(),
        pullStub(pulls, written),
      );
      return { response, written };
    }

    it("is resolved live for a comment, which needs the pull request open", async () => {
      const ok = await commentOn(open7, { ...comment, pull_request: 7 });
      expect(ok.response.status).toBe(200);
      expect(ok.written).toHaveLength(1);
      expect(ok.written[0]?.url).toContain("/issues/7/comments");

      const closed = await commentOn(
        { 7: { state: "closed", sha: "x" } },
        { ...comment, pull_request: 7 },
      );
      expect(closed.response.status).toBe(403);
      expect(closed.written).toHaveLength(0);

      const missing = await commentOn({}, { ...comment, pull_request: 7 });
      expect(missing.response.status).toBe(403);
      expect(missing.written).toHaveLength(0);
    });

    it("requires a positive integer number for a comment", async () => {
      for (const pull_request of [undefined, 0, -1, 1.5, "7"]) {
        const { response, written } = await commentOn(open7, {
          ...comment,
          pull_request,
        });
        expect(response.status).toBe(403);
        expect(written).toHaveLength(0);
      }
    });

    it("leaves a non-clearance run on a non-pull ref refused for a comment", async () => {
      expect(
        (await commentOn(open7, { ...comment, pull_request: 7 }, REFERRAL_REF))
          .response.status,
      ).toBe(403);
      expect(
        (await commentOn(open7, { ...comment, pull_request: 7 }, ""))
          .response.status,
      ).toBe(403);
    });

    it("does not extend to a review", async () => {
      const token = await keys.sign(
        claims({ job_workflow_ref: CLEARANCE_REF, ...clearanceClaims }),
      );
      const response = await send(
        "/review",
        token,
        { version: 1, pull_request: 7 },
        gatedEnv(),
        pullStub(open7),
      );
      expect(response.status).toBe(403);
    });
  });
});
