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
  // individually posted comments are twelve notifications about one push.
  it("opens every new thread in one review, as a comment and never an approval", async () => {
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
    expect(body.comments).toEqual([
      { path: "a.go", body: "one", line: 12 },
      { path: "b.go", body: "two", subject_type: "file" },
    ]);
    expect(outcomes).toEqual([{ op: "create", ref: 2, status: "done" }]);
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

  // A comment somebody removed by hand is already in the state the document
  // asks for.
  it("treats a comment that is already gone as deleted", async () => {
    const { fetcher } = github((_url, method) =>
      method === "DELETE" ? new Response("gone", { status: 404 }) : ok(),
    );
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
