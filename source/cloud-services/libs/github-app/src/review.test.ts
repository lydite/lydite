import { describe, expect, it } from "vitest";

import { applyReview, reviewCommentIds, type ReviewOps } from "./review.js";

interface Call {
  method: string;
  url: string;
  body?: unknown;
}

function github(answers: (url: string, method: string) => Response): {
  fetcher: typeof fetch;
  calls: Call[];
} {
  const calls: Call[] = [];
  const fetcher = (async (url: unknown, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    calls.push({
      method,
      url: String(url),
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    });
    return answers(String(url), method);
  }) as typeof fetch;
  return { fetcher, calls };
}

const ok = () => Response.json({ id: 1 });

describe("the membership set", () => {
  it("walks every page of the pull request's review comments", async () => {
    const page = (from: number) =>
      Response.json(Array.from({ length: 100 }, (_, i) => ({ id: from + i })));
    const { fetcher } = github((url) => (url.endsWith("&page=1") ? page(1) : Response.json([{ id: 500 }])));
    const ids = await reviewCommentIds("t", "lydite/lydite", 7, fetcher);
    expect(ids.size).toBe(101);
    expect(ids.has(500)).toBe(true);
  });

  it("refuses to guess when the listing fails", async () => {
    const { fetcher } = github(() => new Response("no", { status: 500 }));
    await expect(reviewCommentIds("t", "lydite/lydite", 7, fetcher)).rejects.toThrow("500");
  });
});

describe("applying an operations document", () => {
  // A review notifies once however many lines it touches, and twelve
  // individually posted comments are twelve notifications about one push. A
  // file-anchored claim cannot ride in one: a review's comments are drafts
  // with no subjectType field and a required position.
  it("opens line threads in one review and a file thread on its own", async () => {
    const { fetcher, calls } = github(ok);
    const ops: ReviewOps = {
      version: 1,
      head: "abc123",
      create: [
        { path: "a.go", line: 12, subject: "line", body: "one" },
        { path: "b.go", subject: "file", body: "two" },
      ],
    };
    const outcomes = await applyReview("t", "lydite/lydite", 7, ops, fetcher);

    const reviews = calls.filter((c) => c.url.endsWith("/pulls/7/reviews"));
    expect(reviews).toHaveLength(1);
    const body = reviews[0]?.body as { event: string; commit_id: string; comments: unknown[] };
    expect(body.event).toBe("COMMENT");
    expect(body.commit_id).toBe("abc123");
    expect(body.comments).toEqual([{ path: "a.go", body: "one", line: 12 }]);

    const files = calls.filter((c) => c.url.endsWith("/pulls/7/comments"));
    expect(files).toHaveLength(1);
    expect(files[0]?.body).toEqual({
      path: "b.go",
      body: "two",
      subject_type: "file",
      commit_id: "abc123",
    });

    expect(outcomes).toEqual([{ op: "create", ref: 2, status: "done" }]);
  });

  // Nothing to anchor to a line means no review at all, rather than an empty
  // one the platform would refuse.
  it("posts no review when every new thread is about a file", async () => {
    const { fetcher, calls } = github(ok);
    await applyReview(
      "t",
      "lydite/lydite",
      7,
      { version: 1, create: [{ path: "b.go", subject: "file", body: "two" }] },
      fetcher,
    );
    expect(calls.filter((c) => c.url.endsWith("/reviews"))).toHaveLength(0);
    expect(calls).toHaveLength(1);
  });

  it("posts no review when there is nothing to open", async () => {
    const { fetcher, calls } = github(ok);
    await applyReview("t", "lydite/lydite", 7, { version: 1, create: [] }, fetcher);
    expect(calls).toHaveLength(0);
  });

  it("deletes before it replies, so a thread is never answered and then removed", async () => {
    const { fetcher, calls } = github(ok);
    await applyReview(
      "t",
      "lydite/lydite",
      7,
      { version: 1, delete: [{ comment: 5, refused: "r" }], reply: [{ comment: 6, body: "b" }] },
      fetcher,
    );
    expect(calls.map((c) => c.method)).toEqual(["DELETE", "POST"]);
  });

  // Every operation is answered for, so a caller reading the outcomes can
  // tell what the run actually did from what it asked for.
  it("answers for a reply as well as for a delete and a review", async () => {
    const { fetcher } = github(ok);
    const outcomes = await applyReview(
      "t",
      "lydite/lydite",
      7,
      { version: 1, reply: [{ comment: 6, body: "b" }] },
      fetcher,
    );
    expect(outcomes).toEqual([{ op: "reply", ref: 6, status: "done" }]);
  });

  // An identity may only delete what it authored, so a refusal is the
  // handover between lydite's app and a consumer's own bot rather than a
  // malfunction — and the thread is answered instead of vanishing silently.
  it("answers a refused delete with the body the document carries", async () => {
    const { fetcher, calls } = github((_url, method) =>
      method === "DELETE" ? new Response("no", { status: 403 }) : ok(),
    );
    const outcomes = await applyReview(
      "t",
      "lydite/lydite",
      7,
      { version: 1, delete: [{ comment: 5, refused: "it cleared" }] },
      fetcher,
    );
    expect(outcomes).toEqual([{ op: "delete", ref: 5, status: "refused", detail: "answered instead" }]);
    const reply = calls.find((c) => c.url.includes("/comments/5/replies"));
    expect(reply?.body).toEqual({ body: "it cleared" });
  });

  // A delete is refused with 404 rather than 403 where the identity cannot
  // see the comment at all, so both take the answering path.
  it("answers a delete refused as not found", async () => {
    const { fetcher, calls } = github((_url, method) =>
      method === "DELETE" ? new Response("no", { status: 404 }) : ok(),
    );
    const outcomes = await applyReview(
      "t",
      "lydite/lydite",
      7,
      { version: 1, delete: [{ comment: 5, refused: "it cleared" }] },
      fetcher,
    );
    expect(outcomes).toEqual([{ op: "delete", ref: 5, status: "refused", detail: "answered instead" }]);
    expect(calls.some((c) => c.url.includes("/comments/5/replies"))).toBe(true);
  });

  // A reply that is itself unfound settles which of the two a 404 was: the
  // comment is gone, which is the state the delete was asking for.
  it("treats a comment that is already gone as deleted", async () => {
    const { fetcher } = github(() => new Response("gone", { status: 404 }));
    const outcomes = await applyReview("t", "lydite/lydite", 7, { version: 1, delete: [{ comment: 5 }] }, fetcher);
    expect(outcomes).toEqual([{ op: "delete", ref: 5, status: "done" }]);
  });

  it("raises anything else the platform answers", async () => {
    const { fetcher } = github(() => new Response("no", { status: 500 }));
    await expect(
      applyReview("t", "lydite/lydite", 7, { version: 1, create: [{ path: "a.go", line: 1, subject: "line", body: "x" }] }, fetcher),
    ).rejects.toThrow("500");
  });
});

describe("what it refuses to guess about", () => {
  // A page it could not read is a thread it cannot see, and guessing would
  // delete somebody's thread or repost a claim already standing.
  it("raises a delete the platform answered with something else", async () => {
    const { fetcher } = github((_url, method) =>
      method === "DELETE" ? new Response("no", { status: 500 }) : ok(),
    );
    await expect(
      applyReview("t", "lydite/lydite", 7, { version: 1, delete: [{ comment: 5 }] }, fetcher),
    ).rejects.toThrow("500");
  });

  // A reply is the whole of what a thread left standing says, so one that
  // never landed must not read as one that did.
  it("raises a reply to a thread that is not there", async () => {
    const { fetcher } = github(() => new Response("gone", { status: 404 }));
    await expect(
      applyReview("t", "lydite/lydite", 7, { version: 1, reply: [{ comment: 6, body: "b" }] }, fetcher),
    ).rejects.toThrow("404");
  });

  it("raises a reply the platform answered with something else", async () => {
    const { fetcher } = github(() => new Response("no", { status: 500 }));
    await expect(
      applyReview("t", "lydite/lydite", 7, { version: 1, reply: [{ comment: 6, body: "b" }] }, fetcher),
    ).rejects.toThrow("500");
  });

  it("raises a file thread the platform refused", async () => {
    const { fetcher } = github(() => new Response("no", { status: 422 }));
    await expect(
      applyReview(
        "t",
        "lydite/lydite",
        7,
        { version: 1, create: [{ path: "b.go", subject: "file", body: "x" }] },
        fetcher,
      ),
    ).rejects.toThrow("422");
  });
});
