import { describe, expect, it } from "vitest";

import { upsertComment } from "./comment.js";

const MARKER = "<!-- lydite:results -->";

describe("upsertComment", () => {
  // Found by the marker rather than by author, which is what lets the relay
  // take over a comment a consumer's own github-actions[bot] posted first
  // instead of orphaning it.
  it("edits the comment carrying the marker whoever wrote it", async () => {
    const calls: { url: string; method: string }[] = [];
    const outcome = await upsertComment("t", "lydite/lydite", 3, MARKER, "new body", async (url, init) => {
      calls.push({ url: String(url), method: init?.method ?? "GET" });
      if (String(url).includes("/comments?")) {
        return Response.json([
          { id: 1, body: "someone else's comment" },
          { id: 2, body: `${MARKER}\nan earlier verdict` },
        ]);
      }
      return Response.json({ id: 2 });
    });
    expect(outcome).toBe("edited");
    expect(calls.at(-1)).toEqual({
      url: "https://api.github.com/repos/lydite/lydite/issues/comments/2",
      method: "PATCH",
    });
  });

  // The platform's quote-reply copies the raw markdown of the comment it
  // answers, marker and all. Matching one anywhere in a body would make a
  // person quoting lydite's verdict the author of the comment the next run
  // replaces wholesale.
  it("never writes over a comment that merely quotes the marker", async () => {
    const calls: { url: string; method: string }[] = [];
    const outcome = await upsertComment("t", "lydite/lydite", 3, MARKER, "new body", async (url, init) => {
      calls.push({ url: String(url), method: init?.method ?? "GET" });
      if (String(url).includes("/comments?")) {
        return Response.json([{ id: 1, body: `> ${MARKER}\n> the verdict\n\nI disagree` }]);
      }
      return Response.json({ id: 9 });
    });
    expect(outcome).toBe("created");
    expect(calls.at(-1)).toEqual({
      url: "https://api.github.com/repos/lydite/lydite/issues/3/comments",
      method: "POST",
    });
  });

  it("creates one when no comment carries the marker", async () => {
    const outcome = await upsertComment("t", "lydite/lydite", 3, MARKER, "body", async (url, init) => {
      if (String(url).includes("/comments?")) {
        return Response.json([{ id: 1, body: "unrelated" }]);
      }
      expect(init?.method).toBe("POST");
      return Response.json({ id: 9 });
    });
    expect(outcome).toBe("created");
  });
});
