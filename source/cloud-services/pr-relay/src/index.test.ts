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

  it("answers only the two endpoints it has", async () => {
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
    expect(JSON.stringify(await response.json())).not.toContain("upstream detail");
  });
});
