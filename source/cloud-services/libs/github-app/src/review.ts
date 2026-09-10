import { GITHUB_API, apiHeaders } from "./app.js";

/**
 * The operations document lydite's CLI writes and this applies.
 *
 * It is lydite's own wire and not a public shape: the delta that produced it —
 * which fingerprint matches which thread, and which threads lydite is alone in
 * — is computed in the CLI, and this is a transport that decides nothing. A
 * second implementation of that vocabulary in a Worker would be one release
 * behind forever.
 */
export interface ReviewOps {
  version?: number;
  pull_request?: number;
  head?: string;
  create?: CreateOp[];
  reply?: ReplyOp[];
  delete?: DeleteOp[];
}

export interface CreateOp {
  fingerprint?: string;
  path?: string;
  line?: number;
  subject?: string;
  body?: string;
}

export interface ReplyOp {
  comment?: number;
  body?: string;
}

/** A comment to remove, and what to say instead if the platform refuses. */
export interface DeleteOp {
  comment?: number;
  refused?: string;
}

/** The version of the document this understands. */
export const OPS_VERSION = 1;

/** One operation's result, so a posted review and a refused delete are told apart. */
export interface Outcome {
  op: "create" | "reply" | "delete";
  ref?: number | string;
  status: "done" | "refused" | "failed";
  detail?: string;
}

const PAGES = 10;
const PER_PAGE = 100;

/**
 * Every review comment currently on a pull request, by id.
 *
 * This is the membership set the caller checks a document's ids against. A
 * comment id is a number the client supplies and the pull request is the one
 * thing a run cannot choose, so without this the relay would delete a comment
 * on any pull request in the repository on request.
 */
export async function reviewCommentIds(
  token: string,
  repository: string,
  pull: number,
  fetcher: typeof fetch = fetch,
): Promise<Set<number>> {
  const ids = new Set<number>();
  for (let page = 1; page <= PAGES; page++) {
    const response = await fetcher(
      `${GITHUB_API}/repos/${repository}/pulls/${pull}/comments?per_page=${PER_PAGE}&page=${page}`,
      { headers: apiHeaders(`Bearer ${token}`) },
    );
    if (!response.ok) {
      throw new Error(`listing the review comments answered ${response.status}`);
    }
    const comments = (await response.json()) as { id: number }[];
    for (const comment of comments) {
      ids.add(comment.id);
    }
    if (comments.length < PER_PAGE) {
      return ids;
    }
  }
  return ids;
}

/**
 * Applies one operations document, answering per operation.
 *
 * The order is deletions, then replies, then the one review that opens
 * everything new. A run is one review plus N state changes: a review notifies
 * once however many lines it touches, while opening twelve threads
 * individually sends twelve notifications about one push.
 *
 * The event is COMMENT and never REQUEST_CHANGES or APPROVE. Review approval
 * is a different mechanism with different rules about who may give one, and
 * lydite must not touch it.
 *
 * A delete the platform refuses is an outcome rather than a failure: an
 * identity may only delete comments it authored, so a repository that
 * installed the app after its own bot had already written threads has threads
 * neither identity can take down. The document carries what to say in that
 * case, so this composes no prose of its own.
 */
export async function applyReview(
  token: string,
  repository: string,
  pull: number,
  ops: ReviewOps,
  fetcher: typeof fetch = fetch,
): Promise<Outcome[]> {
  const outcomes: Outcome[] = [];

  for (const op of ops.delete ?? []) {
    const id = op.comment as number;
    const response = await fetcher(`${GITHUB_API}/repos/${repository}/pulls/comments/${id}`, {
      method: "DELETE",
      headers: apiHeaders(`Bearer ${token}`),
    });
    if (response.ok || response.status === 404) {
      outcomes.push({ op: "delete", ref: id, status: "done" });
      continue;
    }
    if (response.status !== 403) {
      throw new Error(`deleting a review comment answered ${response.status}`);
    }
    await reply(token, repository, pull, id, op.refused ?? "", fetcher);
    outcomes.push({ op: "delete", ref: id, status: "refused", detail: "answered instead" });
  }

  for (const op of ops.reply ?? []) {
    const id = op.comment as number;
    await reply(token, repository, pull, id, op.body ?? "", fetcher);
    outcomes.push({ op: "reply", ref: id, status: "done" });
  }

  const create = ops.create ?? [];
  if (create.length > 0) {
    const body: Record<string, unknown> = {
      event: "COMMENT",
      comments: create.map((op) =>
        op.subject === "file"
          ? { path: op.path, body: op.body, subject_type: "file" }
          : { path: op.path, body: op.body, line: op.line },
      ),
    };
    if (ops.head) {
      body.commit_id = ops.head;
    }
    const response = await fetcher(`${GITHUB_API}/repos/${repository}/pulls/${pull}/reviews`, {
      method: "POST",
      headers: { ...apiHeaders(`Bearer ${token}`), "content-type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!response.ok) {
      throw new Error(`opening ${create.length} thread(s) answered ${response.status}`);
    }
    outcomes.push({ op: "create", ref: create.length, status: "done" });
  }

  return outcomes;
}

async function reply(
  token: string,
  repository: string,
  pull: number,
  id: number,
  body: string,
  fetcher: typeof fetch,
): Promise<void> {
  const response = await fetcher(
    `${GITHUB_API}/repos/${repository}/pulls/${pull}/comments/${id}/replies`,
    {
      method: "POST",
      headers: { ...apiHeaders(`Bearer ${token}`), "content-type": "application/json" },
      body: JSON.stringify({ body }),
    },
  );
  if (!response.ok) {
    throw new Error(`replying to a review comment answered ${response.status}`);
  }
}
