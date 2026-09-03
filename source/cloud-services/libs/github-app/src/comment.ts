import { GITHUB_API, apiHeaders } from "./app.js";

/**
 * Creates or edits the one comment carrying the marker.
 *
 * Found by a marker in the body rather than by author, because the author is
 * whoever's token posted it — which is the whole point of the relay being able
 * to take over from a consumer's own `github-actions[bot]` without orphaning
 * the comment it already posted.
 *
 * The listing is capped. A conversation long enough that lydite's comment is
 * off the end of it is one where posting a fresh comment is the better failure
 * than editing the wrong one.
 */
export async function upsertComment(
  token: string,
  repository: string,
  issue: number,
  marker: string,
  body: string,
  fetcher: typeof fetch = fetch,
): Promise<"created" | "edited"> {
  const existing = await findComment(token, repository, issue, marker, fetcher);
  const url = existing
    ? `${GITHUB_API}/repos/${repository}/issues/comments/${existing}`
    : `${GITHUB_API}/repos/${repository}/issues/${issue}/comments`;
  const response = await fetcher(url, {
    method: existing ? "PATCH" : "POST",
    headers: { ...apiHeaders(`Bearer ${token}`), "content-type": "application/json" },
    body: JSON.stringify({ body }),
  });
  if (!response.ok) {
    throw new Error(`writing the comment answered ${response.status}`);
  }
  return existing ? "edited" : "created";
}

const PAGES = 10;
const PER_PAGE = 100;

async function findComment(
  token: string,
  repository: string,
  issue: number,
  marker: string,
  fetcher: typeof fetch,
): Promise<number | undefined> {
  for (let page = 1; page <= PAGES; page++) {
    const response = await fetcher(
      `${GITHUB_API}/repos/${repository}/issues/${issue}/comments?per_page=${PER_PAGE}&page=${page}`,
      { headers: apiHeaders(`Bearer ${token}`) },
    );
    if (!response.ok) {
      throw new Error(`listing comments answered ${response.status}`);
    }
    const comments = (await response.json()) as { id: number; body?: string }[];
    const found = comments.find((comment) => comment.body?.includes(marker));
    if (found) {
      return found.id;
    }
    if (comments.length < PER_PAGE) {
      return undefined;
    }
  }
  return undefined;
}
