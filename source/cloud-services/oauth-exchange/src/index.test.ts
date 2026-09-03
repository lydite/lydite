import { describe, expect, it } from "vitest";

import exchange, { type Env } from "./index.js";

const env: Env = {
  LYDITE_DASHBOARD_CLIENT_ID: "Iv1.test",
  LYDITE_DASHBOARD_CLIENT_SECRET: "shh",
};

async function post(body: unknown): Promise<Response> {
  return exchange.fetch(
    new Request("https://app.lydite.org/token", { method: "POST", body: JSON.stringify(body) }),
    env,
  );
}

describe("the dashboard's token exchange", () => {
  it("answers only POST /token", async () => {
    const response = await exchange.fetch(new Request("https://app.lydite.org/"), env);
    expect(response.status).toBe(404);
  });

  it("requires a code or a refresh token", async () => {
    expect((await post({})).status).toBe(400);
  });

  // The App has expiring user tokens on — eight hours, with a six-month
  // refresh — so a flow built around the initial exchange alone works for one
  // working day and then logs everyone out.
  it("accepts a refresh token as well as a code", async () => {
    const sent: Record<string, unknown>[] = [];
    const original = globalThis.fetch;
    globalThis.fetch = (async (_url: unknown, init: RequestInit | undefined) => {
      sent.push(JSON.parse(String(init?.body)));
      return Response.json({ access_token: "ghu_x", refresh_token: "ghr_y", expires_in: 28800 });
    }) as typeof fetch;
    try {
      expect((await post({ code: "abc" })).status).toBe(200);
      expect((await post({ refresh_token: "ghr_old" })).status).toBe(200);
    } finally {
      globalThis.fetch = original;
    }
    expect(sent[0]?.grant_type).toBe("authorization_code");
    expect(sent[1]?.grant_type).toBe("refresh_token");
  });

  // GitHub answers 200 with an error body for a spent grant, so the status
  // alone is not the answer — and the client needs the distinction to know it
  // must send the viewer back through login.
  it("treats a refused grant as a failure even though GitHub answers 200", async () => {
    const original = globalThis.fetch;
    globalThis.fetch = (async () => Response.json({ error: "bad_verification_code" })) as typeof fetch;
    try {
      expect((await post({ code: "spent" })).status).toBe(400);
    } finally {
      globalThis.fetch = original;
    }
  });
});
