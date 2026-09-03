import { encodeBase64Url, encodeJson } from "./base64.js";
import { importSigningKey } from "./pem.js";

export const GITHUB_API = "https://api.github.com";

const UA = "lydite";

/**
 * Signs an App JWT.
 *
 * The App ID is the `iss` claim, and that is the only thing it is ever used
 * for: minting these. `iat` is backdated a minute because GitHub rejects a
 * token whose `iat` is in the future, and a Worker's clock and GitHub's are not
 * the same clock. Ten minutes is GitHub's own ceiling for the lifetime.
 */
export async function appJwt(appId: string, privateKeyPem: string, now = Date.now()): Promise<string> {
  const issued = Math.floor(now / 1000) - 60;
  const header = encodeJson({ alg: "RS256", typ: "JWT" });
  const payload = encodeJson({ iat: issued, exp: issued + 600, iss: appId });
  const key = await importSigningKey(privateKeyPem);
  const signature = await crypto.subtle.sign(
    "RSASSA-PKCS1-v1_5",
    key,
    new TextEncoder().encode(`${header}.${payload}`) as unknown as ArrayBuffer,
  );
  return `${header}.${payload}.${encodeBase64Url(signature)}`;
}

/**
 * Finds the App's installation on a repository, or nothing.
 *
 * A 404 is an answer rather than a failure: it says the consumer has not
 * installed the App, which is the ordinary state for every repository that has
 * not opted in. The caller's response to it is to tell the client to fall back
 * to its own token, not to report an error — a consumer who has not installed
 * anything still gets a comment.
 */
export async function installationId(
  jwt: string,
  repository: string,
  fetcher: typeof fetch = fetch,
): Promise<number | undefined> {
  const response = await fetcher(`${GITHUB_API}/repos/${repository}/installation`, {
    headers: apiHeaders(`Bearer ${jwt}`),
  });
  if (response.status === 404) {
    return undefined;
  }
  if (!response.ok) {
    throw new Error(`looking up the installation answered ${response.status}`);
  }
  const body = (await response.json()) as { id?: number };
  if (!body.id) {
    throw new Error("the installation lookup returned no id");
  }
  return body.id;
}

/**
 * Mints an installation token, narrowed to one repository and one permission.
 *
 * Both narrowings are deliberate. An installation token defaults to every
 * repository the installation covers and every permission the App holds; this
 * one can write pull-request comments on the single repository the verified
 * claims named. So the worst a request can do — including one from a job
 * running a pull request's own code — is write a wrong comment on its own pull
 * request.
 *
 * The token is never stored and never logged. It lives for the one request
 * that mints it.
 */
export async function installationToken(
  jwt: string,
  installation: number,
  repository: string,
  fetcher: typeof fetch = fetch,
): Promise<string> {
  const repo = repository.split("/")[1];
  const response = await fetcher(`${GITHUB_API}/app/installations/${installation}/access_tokens`, {
    method: "POST",
    headers: { ...apiHeaders(`Bearer ${jwt}`), "content-type": "application/json" },
    body: JSON.stringify({
      repositories: repo ? [repo] : undefined,
      permissions: { pull_requests: "write" },
    }),
  });
  if (!response.ok) {
    throw new Error(`minting an installation token answered ${response.status}`);
  }
  const body = (await response.json()) as { token?: string };
  if (!body.token) {
    throw new Error("the token request returned no token");
  }
  return body.token;
}

export function apiHeaders(authorization: string): Record<string, string> {
  return {
    accept: "application/vnd.github+json",
    "x-github-api-version": "2022-11-28",
    "user-agent": UA,
    authorization,
  };
}
