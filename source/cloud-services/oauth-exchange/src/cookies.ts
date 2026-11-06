/**
 * The two cookies the sign-in sets, and nothing else reads or writes them.
 *
 * Both carry the `__Host-` prefix, which a browser honours only for a cookie
 * that is `Secure`, has `Path=/` and names no `Domain` — so neither can be set
 * or overwritten by a sibling subdomain, and neither leaves `app.lydite.org`.
 */

export const LOGIN_COOKIE = "__Host-lydite_login";
export const REFRESH_COOKIE = "__Host-lydite_refresh";

/** Ten minutes: long enough to approve the App on GitHub, short enough that an abandoned sign-in expires. */
export const LOGIN_MAX_AGE = 600;

const BASE64URL = /^[A-Za-z0-9_-]+$/;

/** A refresh token travels as a cookie value verbatim, so it must hold nothing a cookie value cannot. */
const COOKIE_SAFE = /^[A-Za-z0-9._~-]+$/;

export interface LoginBinding {
  state: string;
  verifier: string;
}

/**
 * The cookie binding a sign-in's `state` to its PKCE verifier. `SameSite=Lax`
 * because the callback is a top-level navigation arriving from github.com, and
 * a `Strict` cookie would not be sent on it.
 *
 * The value is the two base64url strings joined by a `.`, which base64url never
 * contains, so it splits back into exactly the pair it was built from.
 */
export function loginCookie({ state, verifier }: LoginBinding): string {
  return `${LOGIN_COOKIE}=${state}.${verifier}; Secure; Path=/; HttpOnly; SameSite=Lax; Max-Age=${LOGIN_MAX_AGE}`;
}

/** The login cookie's pair, or `undefined` for a value that is not exactly two base64url strings. */
export function parseLogin(value: string | undefined): LoginBinding | undefined {
  if (value === undefined) {
    return undefined;
  }
  const parts = value.split(".");
  if (parts.length !== 2) {
    return undefined;
  }
  const [state, verifier] = parts as [string, string];
  if (!BASE64URL.test(state) || !BASE64URL.test(verifier)) {
    return undefined;
  }
  return { state, verifier };
}

/**
 * The cookie holding the viewer's refresh token. `HttpOnly` keeps it out of
 * every script on the page, and `SameSite=Strict` keeps another site from
 * having the browser present it. It lives as long as GitHub says the token does.
 *
 * `undefined` when the token or its lifetime cannot be written as a cookie.
 */
export function refreshCookie(token: string, maxAge: number): string | undefined {
  if (!COOKIE_SAFE.test(token) || !Number.isInteger(maxAge) || maxAge <= 0) {
    return undefined;
  }
  return `${REFRESH_COOKIE}=${token}; Secure; Path=/; HttpOnly; SameSite=Strict; Max-Age=${maxAge}`;
}

/**
 * A header deleting a cookie. A browser ignores a `__Host-` cookie, deletion
 * included, unless it is `Secure` with `Path=/`, so the clear carries both.
 */
export function clearCookie(name: string): string {
  return `${name}=; Secure; Path=/; HttpOnly; Max-Age=0`;
}

/** A cookie's value from the request's `Cookie` header, or `undefined` when it was not sent. */
export function readCookie(request: Request, name: string): string | undefined {
  const header = request.headers.get("cookie");
  if (!header) {
    return undefined;
  }
  for (const pair of header.split(";")) {
    const at = pair.indexOf("=");
    if (at < 0) {
      continue;
    }
    if (pair.slice(0, at).trim() === name) {
      return pair.slice(at + 1).trim();
    }
  }
  return undefined;
}
