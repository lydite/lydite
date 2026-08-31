#!/usr/bin/env python3
"""Assert the verdict lydite must reach on lydite/proving-ground.

The proving ground's correct verdict is not all-green. `scripts/seed.ts` is
under no component and carries no exclude on purpose, so the orphan gate has
to fail on it — and a job asserting only "exit 0" would go green the day the
gate stopped working, which is the one thing a proving ground exists to
prevent. So the assertion names the verdict rather than checking for its
absence: exactly one orphan, that file, and every component's suite passing.

Both halves of the gate are checked, because a repository with one orphan and
no exclusions would leave the exclude path unexercised — and a broken exclude
would then look exactly like a repository that had nothing to exclude.
`generated/client.ts` is a real source file under no component that the
declaration excludes, so it must never appear.

Checking only that it is absent from the report would be vacuous: a file that
has been deleted, renamed, or moved under a component is absent too, and the
exclude path would then go unexercised with the job still green. So its
presence in the checkout is asserted first. The pair — the file is there, and
the gate stayed silent about it — is what says the exclude did the clearing.

It reads --json rather than the terminal report. A text-scraping consumer
makes every refinement to the human surface a two-repository release, which
is the coupling --json exists to remove.
"""

import json
import os
import sys

# Under no component and not excluded: the gate must report it.
EXPECTED_ORPHANS = ["scripts/seed.ts"]
# Under no component and excluded: the gate must stay silent about it.
EXPECTED_EXCLUDED = ["generated/client.ts"]


def main(path: str, checkout: str) -> int:
    with open(path, encoding="utf-8") as fh:
        doc = json.load(fh)
    rows = {r["label"]: r for r in doc["rows"]}
    failures = []

    # The excluded file has to exist for its absence from the report to mean
    # anything. Without this the assertion below passes on a repository that
    # simply deleted it, and the exclude branch stops being exercised without
    # anyone being told.
    for excluded in EXPECTED_EXCLUDED:
        if not os.path.isfile(os.path.join(checkout, excluded)):
            failures.append(
                f"{excluded} is not in the checkout — it exists to prove an exclude clears an "
                "orphan, and without it that half of the gate is untested"
            )

    orphans = rows.get("orphans")
    if orphans is None:
        failures.append("no `orphans` row: the gate did not run at all")
    elif orphans["status"] != "fail":
        failures.append(
            f"orphans row is {orphans['status']!r}, want 'fail' — "
            f"{EXPECTED_ORPHANS[0]} is an orphan by design and a green gate here is a broken gate"
        )
    else:
        # The count comes from the row's value and the paths from its detail,
        # rather than from parsing the whole detail block: the block ends with
        # a sentence telling the author what to do, and asserting on its
        # wording would make every improvement to that sentence a CI edit.
        if not orphans["value"].startswith(f"{len(EXPECTED_ORPHANS)} "):
            failures.append(
                f"orphans value is {orphans['value']!r}, want {len(EXPECTED_ORPHANS)} — "
                "a new orphan here is either a real gap or a change this assertion should record"
            )
        for want in EXPECTED_ORPHANS:
            if want not in orphans.get("detail", []):
                failures.append(f"orphans detail does not name {want}")
        for excluded in EXPECTED_EXCLUDED:
            if excluded in orphans.get("detail", []):
                failures.append(
                    f"{excluded} is reported as an orphan, but the declaration excludes it — "
                    "the exclude is not clearing anything"
                )

    # Every component still has to pass. The orphan gate failing must not be
    # able to mask a runner that stopped working, which is what this job was
    # built to catch in the first place.
    suites = {label: r for label, r in rows.items() if label.startswith("test(")}
    if not suites:
        failures.append("no component rows: nothing ran")
    for label, r in sorted(suites.items()):
        if r["status"] != "pass":
            failures.append(f"{label} is {r['status']!r}: {r.get('value', '')}")

    for f in failures:
        print(f"proving ground: {f}", file=sys.stderr)
    if failures:
        return 1
    print(
        f"proving ground: {len(suites)} component(s) passed; "
        f"orphan gate fired on {EXPECTED_ORPHANS} and stayed silent on {EXPECTED_EXCLUDED}"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1], sys.argv[2]))
