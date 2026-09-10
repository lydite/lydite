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

const PER_PAGE = 100;

/**
 * The pages the walk visits, as a list rather than a loop bound.
 *
 * A `for (let page = 1; page <= PAGES; page++)` carries a boundary and a step
 * that can each be shifted without any listing noticing; a list of the page
 * numbers has neither.
 */
const PAGES = Array.from({ length: 10 }, (_, index) => index + 1);

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
  for (const page of PAGES) {
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
 * The order is deletions, then replies, then the threads to open. A run is one
 * review plus N state changes: a review notifies once however many lines it
 * touches, while opening twelve threads individually sends twelve
 * notifications about one push.
 *
 * The event is COMMENT and never REQUEST_CHANGES or APPROVE. Review approval
 * is a different mechanism with different rules about who may give one, and
 * lydite must not touch it. The review carries no body of its own: a summary
 * above the threads would be a second standing verdict beside the comment that
 * already carries one.
 *
 * A file-anchored claim is posted on its own. A review's comments are drafts
 * with no `subjectType` field and a required position, so the API refuses one
 * inside a review and accepts it as a comment of its own.
 *
 * A delete the platform will not do is an outcome rather than a failure: an
 * identity may only delete comments it authored, so a repository that
 * installed the app after its own bot had already written threads has threads
 * neither identity can take down. It answers 403 where this identity can see
 * the comment and 404 where it cannot, so both are answered with a reply — and
 * a reply that is itself unfound settles that the comment is simply gone. The
 * document carries what to say, so this composes no prose of its own.
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
    if (response.ok) {
      outcomes.push({ op: "delete", ref: id, status: "done" });
      continue;
    }
    if (response.status !== 403 && response.status !== 404) {
      throw new Error(`deleting a review comment answered ${response.status}`);
    }
    const answered = await reply(token, repository, pull, id, op.refused ?? "", fetcher);
    outcomes.push(
      answered
        ? { op: "delete", ref: id, status: "refused", detail: "answered instead" }
        : { op: "delete", ref: id, status: "done" },
    );
  }

  for (const op of ops.reply ?? []) {
    const id = op.comment as number;
    if (!(await reply(token, repository, pull, id, op.body ?? "", fetcher))) {
      throw new Error("replying to a review comment answered 404");
    }
    outcomes.push({ op: "reply", ref: id, status: "done" });
  }

  const create = ops.create ?? [];
  const onLines = create.filter((op) => op.subject !== "file");
  if (onLines.length > 0) {
    const body: Record<string, unknown> = {
      event: "COMMENT",
      comments: onLines.map((op) => ({ path: op.path, body: op.body, line: op.line })),
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
      throw new Error(`opening ${onLines.length} thread(s) answered ${response.status}`);
    }
  }

  for (const op of create.filter((each) => each.subject === "file")) {
    const body: Record<string, unknown> = { path: op.path, body: op.body, subject_type: "file" };
    if (ops.head) {
      body.commit_id = ops.head;
    }
    const response = await fetcher(`${GITHUB_API}/repos/${repository}/pulls/${pull}/comments`, {
      method: "POST",
      headers: { ...apiHeaders(`Bearer ${token}`), "content-type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!response.ok) {
      throw new Error(`opening a thread on a file answered ${response.status}`);
    }
  }

  if (create.length > 0) {
    outcomes.push({ op: "create", ref: create.length, status: "done" });
  }

  return outcomes;
}

/** Replies to one thread, answering false when the comment is not there. */
async function reply(
  token: string,
  repository: string,
  pull: number,
  id: number,
  body: string,
  fetcher: typeof fetch,
): Promise<boolean> {
  const response = await fetcher(
    `${GITHUB_API}/repos/${repository}/pulls/${pull}/comments/${id}/replies`,
    {
      method: "POST",
      headers: { ...apiHeaders(`Bearer ${token}`), "content-type": "application/json" },
      body: JSON.stringify({ body }),
    },
  );
  if (response.status === 404) {
    return false;
  }
  if (!response.ok) {
    throw new Error(`replying to a review comment answered ${response.status}`);
  }
  return true;
}
