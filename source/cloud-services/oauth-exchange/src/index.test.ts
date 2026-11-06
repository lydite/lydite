import { afterEach, describe, expect, it } from "vitest";

import { LOGIN_COOKIE, REFRESH_COOKIE } from "./cookies.js";
import exchange, { type Env } from "./index.js";
import { challengeFor } from "./pkce.js";

const env: Env = {
  LYDITE_DASHBOARD_CLIENT_ID: "Iv1.test",
  LYDITE_DASHBOARD_CLIENT_SECRET: "shh",
};

const ORIGIN = "https://app.lydite.org";
const STATE = "s".repeat(43);
const VERIFIER = "v".repeat(43);
const LOGIN_CLEARED = `${LOGIN_COOKIE}=; Secure; Path=/; HttpOnly; Max-Age=0`;

const original = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = original;
});

interface Sent {
  url: string;
  body: Record<string, unknown>;
}

// Answers GitHub's token endpoint locally, recording what was sent. A test
// that let the call reach the network would be testing GitHub.
function github(answer: () => Response | Promise<Response>): Sent[] {
  const sent: Sent[] = [];
  globalThis.fetch = (async (url: unknown, init?: RequestInit) => {
    sent.push({ url: String(url), body: JSON.parse(String(init?.body)) });
    return answer();
  }) as typeof fetch;
  return sent;
}

const granted = () =>
  Response.json({
    access_token: "ghu_access",
    expires_in: 28800,
    refresh_token: "ghr_refresh",
    refresh_token_expires_in: 15811200,
    token_type: "bearer",
  });

function get(path: string, cookie?: string): Promise<Response> {
  return exchange.fetch(
    new Request(`${ORIGIN}${path}`, { headers: cookie ? { cookie } : {} }),
    env,
  );
}

function callback(query: string, cookie: string | undefined = `${LOGIN_COOKIE}=${STATE}.${VERIFIER}`) {
  return get(`/auth/callback?${query}`, cookie);
}

function cookies(response: Response): string[] {
  return response.headers.getSetCookie();
}

function refreshCookieOf(response: Response): string | undefined {
  return cookies(response).find((c) => c.startsWith(`${REFRESH_COOKIE}=`));
}

describe("the routes the Worker answers", () => {
  it("answers 404 at the root, which is the app's and not the Worker's", async () => {
    expect((await get("/")).status).toBe(404);
  });

  it("answers 404 to POST /token", async () => {
    const response = await exchange.fetch(
      new Request(`${ORIGIN}/token`, { method: "POST", body: JSON.stringify({ code: "abc" }) }),
      env,
    );
    expect(response.status).toBe(404);
  });

  it("answers 404 to a login route reached with the wrong method", async () => {
    const response = await exchange.fetch(new Request(`${ORIGIN}/auth/login`, { method: "POST" }), env);
    expect(response.status).toBe(404);
  });
});

describe("GET /auth/login", () => {
  it("sets a Lax, HttpOnly, ten-minute login cookie holding a state and a verifier", async () => {
    const response = await get("/auth/login");
    expect(response.status).toBe(302);
    const [cookie, ...rest] = cookies(response);
    expect(rest).toEqual([]);
    expect(cookie).toMatch(
      new RegExp(`^${LOGIN_COOKIE}=[A-Za-z0-9_-]{43}\\.[A-Za-z0-9_-]{43}; Secure; Path=/; HttpOnly; SameSite=Lax; Max-Age=600$`),
    );
  });

  it("redirects to GitHub with the client id, callback, state and an S256 challenge of the cookie's verifier", async () => {
    const response = await get("/auth/login");
    const [cookie] = cookies(response);
    const [state, verifier] = (cookie ?? "").split(";")[0]!.split("=")[1]!.split(".") as [string, string];

    const location = new URL(response.headers.get("location") ?? "");
    expect(`${location.origin}${location.pathname}`).toBe("https://github.com/login/oauth/authorize");
    expect(location.searchParams.get("client_id")).toBe("Iv1.test");
    expect(location.searchParams.get("redirect_uri")).toBe(`${ORIGIN}/auth/callback`);
    expect(location.searchParams.get("state")).toBe(state);
    expect(location.searchParams.get("code_challenge_method")).toBe("S256");
    expect(location.searchParams.get("code_challenge")).toBe(await challengeFor(verifier));
    expect(location.searchParams.get("code_challenge")).not.toBe(verifier);
  });

  it("draws a fresh state and verifier for every sign-in", async () => {
    const first = cookies(await get("/auth/login"))[0];
    const second = cookies(await get("/auth/login"))[0];
    expect(first).not.toBe(second);
  });
});

describe("GET /auth/callback", () => {
  it("sends a browser with no login cookie back to sign in, without exchanging the code", async () => {
    const sent = github(granted);
    const response = await callback(`code=abc&state=${STATE}`, "");
    expect(response.status).toBe(302);
    expect(response.headers.get("location")).toBe("/auth/login");
    expect(cookies(response)).toEqual([LOGIN_CLEARED]);
    expect(sent).toEqual([]);
  });

  it("treats a malformed login cookie as no login cookie", async () => {
    const sent = github(granted);
    const response = await callback(`code=abc&state=${STATE}`, `${LOGIN_COOKIE}=${STATE}`);
    expect(response.status).toBe(302);
    expect(response.headers.get("location")).toBe("/auth/login");
    expect(cookies(response)).toEqual([LOGIN_CLEARED]);
    expect(sent).toEqual([]);
  });

  it("refuses a callback with no state, echoing nothing", async () => {
    const sent = github(granted);
    const response = await callback("code=abc");
    expect(response.status).toBe(400);
    expect(cookies(response)).toEqual([LOGIN_CLEARED]);
    expect(await response.text()).not.toContain("abc");
    expect(sent).toEqual([]);
  });

  it("refuses a state that does not match the login cookie's, echoing neither", async () => {
    const sent = github(granted);
    const response = await callback("code=abc&state=forged");
    expect(response.status).toBe(400);
    expect(cookies(response)).toEqual([LOGIN_CLEARED]);
    const body = await response.text();
    expect(body).not.toContain("forged");
    expect(body).not.toContain(STATE);
    expect(body).not.toContain("abc");
    expect(sent).toEqual([]);
  });

  it("returns to the app when GitHub reports the viewer declined", async () => {
    const sent = github(granted);
    const response = await callback(`error=access_denied&state=${STATE}`);
    expect(response.status).toBe(302);
    expect(response.headers.get("location")).toBe("/");
    expect(cookies(response)).toEqual([LOGIN_CLEARED]);
    expect(sent).toEqual([]);
  });

  it("returns to the app when GitHub's redirect carries no code", async () => {
    const sent = github(granted);
    const response = await callback(`state=${STATE}`);
    expect(response.status).toBe(302);
    expect(response.headers.get("location")).toBe("/");
    expect(cookies(response)).toEqual([LOGIN_CLEARED]);
    expect(sent).toEqual([]);
  });

  it("answers 502 when GitHub cannot be reached", async () => {
    github(() => {
      throw new TypeError("network down");
    });
    const response = await callback(`code=abc&state=${STATE}`);
    expect(response.status).toBe(502);
    expect(cookies(response)).toEqual([LOGIN_CLEARED]);
  });

  it("answers 502 when GitHub answers a non-2xx status", async () => {
    github(() => new Response("unavailable", { status: 503 }));
    const response = await callback(`code=abc&state=${STATE}`);
    expect(response.status).toBe(502);
    expect(cookies(response)).toEqual([LOGIN_CLEARED]);
  });

  it("answers 502 when GitHub's reply is not a JSON object", async () => {
    github(() => new Response("<html>", { status: 200 }));
    const response = await callback(`code=abc&state=${STATE}`);
    expect(response.status).toBe(502);
    expect(cookies(response)).toEqual([LOGIN_CLEARED]);
  });

  // GitHub answers 200 with an error body for a spent grant, so the status
  // alone is not the answer.
  it("refuses a grant GitHub refused even though it answered 200", async () => {
    github(() => Response.json({ error: "bad_verification_code" }));
    const response = await callback(`code=spent&state=${STATE}`);
    expect(response.status).toBe(400);
    expect(cookies(response)).toEqual([LOGIN_CLEARED]);
    expect(await response.text()).not.toContain("spent");
  });

  it("sends the code, the verifier and the callback to GitHub's token endpoint", async () => {
    const sent = github(granted);
    await callback(`code=abc&state=${STATE}`);
    expect(sent).toHaveLength(1);
    expect(sent[0]?.url).toBe("https://github.com/login/oauth/access_token");
    expect(sent[0]?.body).toEqual({
      client_id: "Iv1.test",
      client_secret: "shh",
      code: "abc",
      code_verifier: VERIFIER,
      redirect_uri: `${ORIGIN}/auth/callback`,
    });
  });

  it("keeps the refresh token in a Strict HttpOnly cookie and returns to the app carrying no token", async () => {
    github(granted);
    const response = await callback(`code=abc&state=${STATE}`);
    expect(response.status).toBe(302);
    expect(response.headers.get("location")).toBe("/");
    expect(cookies(response)).toEqual([
      LOGIN_CLEARED,
      `${REFRESH_COOKIE}=ghr_refresh; Secure; Path=/; HttpOnly; SameSite=Strict; Max-Age=15811200`,
    ]);
    const body = await response.text();
    for (const token of ["ghu_access", "ghr_refresh"]) {
      expect(body).not.toContain(token);
      expect(response.headers.get("location")).not.toContain(token);
    }
  });

  it("answers 502 and sets no refresh cookie when GitHub's reply carries no refresh token", async () => {
    github(() => Response.json({ access_token: "ghu_access", token_type: "bearer" }));
    const response = await callback(`code=abc&state=${STATE}`);
    expect(response.status).toBe(502);
    expect(refreshCookieOf(response)).toBeUndefined();
    expect(cookies(response)).toEqual([LOGIN_CLEARED]);
    expect(await response.text()).not.toContain("ghu_access");
  });

  it("answers 502 and sets no refresh cookie when GitHub's reply gives the refresh token no lifetime", async () => {
    github(() => Response.json({ access_token: "ghu_access", refresh_token: "ghr_refresh" }));
    const response = await callback(`code=abc&state=${STATE}`);
    expect(response.status).toBe(502);
    expect(refreshCookieOf(response)).toBeUndefined();
    expect(cookies(response)).toEqual([LOGIN_CLEARED]);
    expect(await response.text()).not.toContain("ghr_refresh");
  });
});
