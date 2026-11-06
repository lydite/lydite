import {
  clearCookie,
  LOGIN_COOKIE,
  loginCookie,
  parseLogin,
  REFRESH_COOKIE,
  readCookie,
  refreshCookie,
} from "./cookies.js";
import { challengeFor, randomToken, sameText } from "./pkce.js";

export interface Env {
  LYDITE_DASHBOARD_CLIENT_ID: string;
  LYDITE_DASHBOARD_CLIENT_SECRET: string;
}

const GITHUB_AUTHORIZE_URL = "https://github.com/login/oauth/authorize";
const GITHUB_TOKEN_URL = "https://github.com/login/oauth/access_token";

const CALLBACK_PATH = "/auth/callback";
const LOGIN_PATH = "/auth/login";
const LOGOUT_PATH = "/auth/logout";
const REFRESH_PATH = "/auth/refresh";

type Handler = (request: Request, env: Env) => Promise<Response>;

/** Every route the Worker answers, keyed by method and path; anything else is 404. */
const ROUTES: Record<string, Handler> = {
  [`GET ${LOGIN_PATH}`]: login,
  [`GET ${CALLBACK_PATH}`]: callback,
  [`POST ${REFRESH_PATH}`]: sameOrigin(refresh),
  [`POST ${LOGOUT_PATH}`]: sameOrigin(logout),
};

/**
 * The dashboard's sign-in, served on `app.lydite.org` beside the app itself.
 *
 * The Worker drives the whole OAuth round trip, because the client secret
 * cannot live in a static bundle: `/auth/login` sends the viewer to GitHub with
 * a fresh `state` and a PKCE S256 challenge, both bound to this browser by an
 * `HttpOnly` cookie, and `/auth/callback` checks the `state`, exchanges the code
 * with the verifier, and keeps only the refresh token — in an `HttpOnly`,
 * `SameSite=Strict` cookie no script on the page can read. The first access
 * token GitHub returns is discarded rather than handed over: no token ever
 * appears in a URL, and the refresh token never in a response body.
 *
 * `/auth/refresh` trades that cookie for an access token, the only response
 * body any token appears in, and `/auth/logout` clears it. Both act on a
 * cookie the browser attaches by itself, so both answer only a request whose
 * `Origin` is this one; no CORS header is ever sent, so no other origin can
 * read what either answers.
 *
 * It never reads a repository — the dashboard reads the ledger with the
 * *viewer's* credentials, which is what ADR 0009 means by lydite holding none
 * of your data — and it stores and logs nothing. Every rejection carries a
 * generic body: an answer that echoed a code, a state or a token would put
 * them wherever responses end up.
 */
export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const handler = ROUTES[`${request.method} ${new URL(request.url).pathname}`];
    if (!handler) {
      return json(404, { error: "not found" });
    }
    return handler(request, env);
  },
};

async function login(request: Request, env: Env): Promise<Response> {
  const state = randomToken();
  const verifier = randomToken();
  const authorize = new URL(GITHUB_AUTHORIZE_URL);
  authorize.searchParams.set("client_id", env.LYDITE_DASHBOARD_CLIENT_ID);
  authorize.searchParams.set("redirect_uri", callbackUrl(request));
  authorize.searchParams.set("state", state);
  authorize.searchParams.set("code_challenge", await challengeFor(verifier));
  authorize.searchParams.set("code_challenge_method", "S256");
  return redirect(authorize.toString(), [loginCookie({ state, verifier })]);
}

/**
 * GitHub's redirect back. The login cookie is single-use, so every outcome
 * clears it: a sign-in that failed starts over from `/auth/login` with a fresh
 * pair rather than retrying with one a failed attempt has already exposed.
 */
async function callback(request: Request, env: Env): Promise<Response> {
  const cleared = clearCookie(LOGIN_COOKIE);
  const binding = parseLogin(readCookie(request, LOGIN_COOKIE));
  if (!binding) {
    // No sign-in was started from this browser, or it expired: the code is
    // never exchanged, since nothing here binds it to the viewer presenting it.
    return redirect(LOGIN_PATH, [cleared]);
  }

  const params = new URL(request.url).searchParams;
  const state = params.get("state");
  if (state === null || !sameText(state, binding.state)) {
    return json(400, { error: "the sign-in could not be verified" }, [cleared]);
  }

  const code = params.get("code");
  if (params.has("error") || !code) {
    // The viewer declined on GitHub, or GitHub answered with nothing to exchange.
    return redirect("/", [cleared]);
  }

  const token = await exchange(env, {
    code,
    code_verifier: binding.verifier,
    redirect_uri: callbackUrl(request),
  });
  if (!token) {
    return json(502, { error: "the token exchange failed" }, [cleared]);
  }
  if (typeof token.error === "string") {
    // GitHub answers 200 with an error body for a spent or invalid grant, so
    // the status alone is not the answer.
    return json(400, { error: "the grant was refused" }, [cleared]);
  }

  const refresh =
    typeof token.refresh_token === "string" && typeof token.refresh_token_expires_in === "number"
      ? refreshCookie(token.refresh_token, token.refresh_token_expires_in)
      : undefined;
  if (!refresh) {
    // A token with no refresh token, or no lifetime for it, is an App without
    // expiring user tokens: the session it would start could never be renewed.
    return json(502, { error: "the token exchange failed" }, [cleared]);
  }
  return redirect("/", [cleared, refresh]);
}

/**
 * Trades the refresh cookie for a fresh access token, and rotates the cookie to
 * the refresh token GitHub issues with it. The body carries the access token
 * alone: the refresh token never leaves the cookie.
 */
async function refresh(request: Request, env: Env): Promise<Response> {
  const current = readCookie(request, REFRESH_COOKIE);
  if (!current) {
    return json(401, { error: "not signed in" });
  }

  const token = await exchange(env, { grant_type: "refresh_token", refresh_token: current });
  if (!token) {
    return json(502, { error: "the token refresh failed" });
  }
  if (typeof token.error === "string") {
    // The cookie is left as it is. GitHub rotates the refresh token on every
    // grant, so a refused one is most often a token another tab has just
    // spent — and that tab's response has already set its successor here.
    // Clearing it would sign every tab out.
    return json(401, { error: "the session could not be renewed" });
  }

  const rotated =
    typeof token.refresh_token === "string" && typeof token.refresh_token_expires_in === "number"
      ? refreshCookie(token.refresh_token, token.refresh_token_expires_in)
      : undefined;
  if (typeof token.access_token !== "string" || !rotated) {
    return json(502, { error: "the token refresh failed" });
  }
  return json(
    200,
    { access_token: token.access_token, expires_in: token.expires_in, token_type: token.token_type },
    [rotated],
  );
}

/**
 * Clears the refresh cookie. The token is not revoked at GitHub: it expires on
 * its own, and with the cookie gone nothing on this origin can present it.
 */
async function logout(): Promise<Response> {
  return new Response(null, {
    status: 204,
    headers: { "cache-control": "no-store", "set-cookie": clearCookie(REFRESH_COOKIE) },
  });
}

/**
 * A handler that answers only a request whose `Origin` is exactly the one it
 * arrived at. The refresh cookie is `SameSite=Strict`, and this is the check
 * that does not depend on the browser honouring that. A request with no
 * `Origin` is refused, since a browser sends one on every cross-origin POST
 * and the dashboard's own fetch sends one too.
 */
function sameOrigin(handler: Handler): Handler {
  return async (request, env) => {
    if (request.headers.get("origin") !== new URL(request.url).origin) {
      return json(403, { error: "forbidden" });
    }
    return handler(request, env);
  };
}

/**
 * GitHub's token reply to a grant, as an object, or `undefined` when the
 * exchange did not produce one. The client's own credentials are added here;
 * `grant` carries the rest.
 */
async function exchange(env: Env, grant: Record<string, string>): Promise<Record<string, unknown> | undefined> {
  let response: Response;
  try {
    response = await fetch(GITHUB_TOKEN_URL, {
      method: "POST",
      headers: { accept: "application/json", "content-type": "application/json" },
      body: JSON.stringify({
        client_id: env.LYDITE_DASHBOARD_CLIENT_ID,
        client_secret: env.LYDITE_DASHBOARD_CLIENT_SECRET,
        ...grant,
      }),
    });
  } catch {
    return undefined;
  }
  if (!response.ok) {
    return undefined;
  }
  const body: unknown = await response.json().catch(() => undefined);
  if (typeof body !== "object" || body === null || Array.isArray(body)) {
    return undefined;
  }
  return body as Record<string, unknown>;
}

/** The callback on the origin the request arrived at, so the authorize and the exchange name the same one. */
function callbackUrl(request: Request): string {
  return `${new URL(request.url).origin}${CALLBACK_PATH}`;
}

function redirect(location: string, cookies: string[]): Response {
  const headers = new Headers({ location, "cache-control": "no-store" });
  for (const cookie of cookies) {
    headers.append("set-cookie", cookie);
  }
  return new Response(null, { status: 302, headers });
}

function json(status: number, body: unknown, cookies: string[] = []): Response {
  const headers = new Headers({ "content-type": "application/json", "cache-control": "no-store" });
  for (const cookie of cookies) {
    headers.append("set-cookie", cookie);
  }
  return new Response(JSON.stringify(body), { status, headers });
}
