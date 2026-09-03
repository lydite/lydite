export interface Env {
  LYDITE_DASHBOARD_CLIENT_ID: string;
  LYDITE_DASHBOARD_CLIENT_SECRET: string;
}

const GITHUB_TOKEN_URL = "https://github.com/login/oauth/access_token";

/**
 * The dashboard's token exchange.
 *
 * It does one thing: turn a code or a refresh token into a token, because the
 * client secret cannot live in a static bundle. It never reads a repository —
 * the dashboard reads the ledger with the *viewer's* credentials, which is what
 * ADR 0009 means by lydite holding none of your data — and it stores nothing.
 *
 * The refresh half is not an afterthought. The dashboard App has expiring user
 * tokens on, so an access token lasts eight hours and a refresh token six
 * months; a flow built around the initial exchange alone works for one working
 * day and then logs everyone out. Both grants go through one handler because
 * they are the same request to GitHub with a different grant type, and two
 * handlers would be two places to get the secret handling wrong.
 *
 * It is a scaffold: the dashboard it serves is ADR 0009's and is not built. The
 * two Apps and their key custody are the decision this delivers, and a relay
 * that could read repository contents would be that decision unmade.
 */
export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    if (request.method !== "POST" || url.pathname !== "/token") {
      return json(404, { error: "POST /token" });
    }

    const body = (await request.json().catch(() => ({}))) as {
      code?: string;
      refresh_token?: string;
    };
    const grant = grantFor(body);
    if (!grant) {
      return json(400, { error: "either a code or a refresh_token is required" });
    }

    const response = await fetch(GITHUB_TOKEN_URL, {
      method: "POST",
      headers: { accept: "application/json", "content-type": "application/json" },
      body: JSON.stringify({
        client_id: env.LYDITE_DASHBOARD_CLIENT_ID,
        client_secret: env.LYDITE_DASHBOARD_CLIENT_SECRET,
        ...grant,
      }),
    });
    if (!response.ok) {
      return json(502, { error: "the token exchange failed" });
    }
    const token = (await response.json()) as Record<string, unknown>;
    if (typeof token.error === "string") {
      // GitHub answers 200 with an error body for a spent or invalid grant, so
      // the status alone is not the answer. The client's move is to send the
      // viewer back through login, which needs the distinction.
      return json(400, { error: "the grant was refused" });
    }
    // Relayed as GitHub returned it, minus nothing and plus nothing. The
    // expiry and the refresh token are the client's to manage, and a Worker
    // that reshaped them would be a second place the token lifetime is
    // described.
    return json(200, token);
  },
};

function grantFor(body: { code?: string; refresh_token?: string }): Record<string, string> | undefined {
  if (body.code) {
    return { code: body.code, grant_type: "authorization_code" };
  }
  if (body.refresh_token) {
    return { refresh_token: body.refresh_token, grant_type: "refresh_token" };
  }
  return undefined;
}

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}
