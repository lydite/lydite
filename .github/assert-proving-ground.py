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

The same standard applies to the scheduler. `tally` and `api` both publish host
port 5432, under services named `postgres` and `db` — differently on purpose, so a lock keyed on service names passes this repository while being
wrong. Asserting the exit code would prove nothing: a scheduler that ran
everything one at a time satisfies every claim about port locks without once
having taken one. So the observed concurrency and the serialised pair are
asserted, not the absence of a failure.

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
import re
import sys

# Under no component and not excluded: the gate must report it.
EXPECTED_ORPHANS = ["scripts/seed.ts"]
# Under no component and excluded: the gate must stay silent about it.
EXPECTED_EXCLUDED = ["generated/client.ts"]
# The components rooted at `rust/` and `go/api/` both publish this host port,
# under services deliberately named differently (`postgres` and `db`), so a
# lock keyed on service names would miss the collision and fail mid-run on a
# bind error.
CONTENDED_PORT = 5432
EXPECTED_SERIALISED = ("tally", "api")

# The selection probe commits a change under go/api/ and asserts what runs.
# api is selected by its own directory; sdk and web hold a client generated
# from the spec api emits, an edge across three languages that no build tool
# can derive and that only `depends_on` states. tally shares nothing with any
# of them and must not run.
#
# Asserting the run was green would prove nothing here: a selection that
# returned every component passes that, and delivers none of what selecting is
# for. So the components that must NOT have run are named too, and their
# absence from the suites is what is checked.
SELECTION_PROBE = "go/api/selection_probe.go"
EXPECTED_AFFECTED = ["api", "sdk", "web"]
EXPECTED_UNAFFECTED = ["tally"]
# sdk and web are reachable only through the edge, so the reason each carries
# is the one thing distinguishing a working transitive closure from a
# selection that happened to return three components.
EXPECTED_VIA = {"sdk": "depends on api", "web": "depends on api"}


def concurrency(value: str) -> int | None:
    """Read the components-run-at-once count out of the schedule row's value.

    The count is taken from the value rather than the detail because the detail
    ends with prose, and asserting on its wording would make every improvement
    to that sentence a CI edit.
    """
    match = re.search(r"max (\d+) concurrent", value)
    return int(match.group(1)) if match else None


def main_affected(path: str, checkout: str) -> int:
    """Assert which components a change under go/api/ selected."""
    with open(path, encoding="utf-8") as fh:
        doc = json.load(fh)
    rows = {r["label"]: r for r in doc["rows"]}
    failures = []

    if not os.path.isfile(os.path.join(checkout, SELECTION_PROBE)):
        failures.append(
            f"{SELECTION_PROBE} is not in the checkout — the probe never made a diff, so every "
            "assertion below is about a run with nothing to select"
        )

    select = rows.get("select")
    if select is None:
        failures.append("no `select` row: the run did not narrow, or did not say that it had")
    else:
        want = f"{len(EXPECTED_AFFECTED)} of {len(EXPECTED_AFFECTED) + len(EXPECTED_UNAFFECTED)} affected"
        if select.get("value") != want:
            failures.append(f"select value is {select.get('value')!r}, want {want!r}")
        if select.get("status") != "pass":
            failures.append(f"select status is {select.get('status')!r}, want 'pass'")
        detail = select.get("detail", [])
        for name, reason in sorted(EXPECTED_VIA.items()):
            if f"{name}: {reason}" not in detail:
                failures.append(
                    f"select detail {detail!r} does not record {name!r} as {reason!r} — that edge is "
                    "declared because no tool can derive it, and nothing else here would notice it "
                    "having stopped working"
                )

    suites = {label for label in rows if label.startswith("test(")}
    for name in EXPECTED_AFFECTED:
        if f"test({name})" not in suites:
            failures.append(f"{name} did not run, but a change under go/api/ must select it")
        elif rows[f"test({name})"]["status"] != "pass":
            failures.append(f"test({name}) is {rows[f'test({name})']['status']!r}")
    for name in EXPECTED_UNAFFECTED:
        if f"test({name})" in suites:
            failures.append(
                f"{name} ran, but nothing in the change touches it — a selection that returns "
                "every component satisfies every assertion about correctness and delivers none "
                "of the value"
            )
        row = rows.get(name)
        if row is None:
            failures.append(
                f"no {name!r} row: a deselected component must be reported rather than dropped, "
                "or a reader cannot tell 'not affected' from 'not declared'"
            )
        elif row.get("status") != "unmeasured" or row.get("value") != "not affected":
            failures.append(f"{name} row is {row!r}, want an unmeasured 'not affected' row")

    # Every watch pattern the declaration carries names a file that exists, so
    # this gate has to be silent. A failure here means either a watched file
    # was deleted without the declaration being updated, or the gate itself
    # started firing on correct declarations.
    watch = rows.get("watch")
    if watch is None:
        failures.append("no `watch` row: the gate did not run")
    elif watch.get("status") != "pass":
        failures.append(f"watch row is {watch!r}, want a pass — every watched file exists here")

    for f in failures:
        print(f"proving ground (affected): {f}", file=sys.stderr)
    if failures:
        return 1
    print(
        f"proving ground (affected): a change under go/api/ selected {EXPECTED_AFFECTED} "
        f"and skipped {EXPECTED_UNAFFECTED}; {sorted(EXPECTED_VIA)} came through the "
        "declared dependency edge"
    )
    return 0


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

    # The scheduler has to be shown to have scheduled. Every assertion about
    # port locks is satisfied by a run that never had two components going at
    # once, because the lock is never taken — so a green job would prove
    # nothing about a constraint that was never reached.
    schedule = rows.get("schedule")
    if schedule is None:
        failures.append("no `schedule` row: the scheduler did not report what it did")
    else:
        concurrent = concurrency(schedule.get("value", ""))
        if concurrent is None:
            failures.append(f"schedule value {schedule.get('value')!r} names no observed concurrency")
        elif concurrent < 2:
            failures.append(
                f"schedule reports max {concurrent} concurrent: nothing ever ran at once, so the "
                "port lock was never contended and every assertion about it here is vacuous"
            )
        # One whole line has to say it, in either order. Joining the detail
        # and testing for each name separately passes on a report whose lines
        # are "tally and web ... 5432" and "api and db ... 6379" — neither of
        # which is the pair this exists to observe.
        a, b = EXPECTED_SERIALISED
        want = re.compile(
            rf"(?:{re.escape(a)} and {re.escape(b)}|{re.escape(b)} and {re.escape(a)})"
            rf" serialised on port {CONTENDED_PORT}$"
        )
        if not any(want.match(line) for line in schedule.get("detail", [])):
            failures.append(
                f"schedule detail {schedule.get('detail')!r} does not record {a} and {b} being "
                f"serialised on port {CONTENDED_PORT} — they publish it under differently named "
                "services, so a lock keyed on names would miss them"
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
        f"orphan gate fired on {EXPECTED_ORPHANS} and stayed silent on {EXPECTED_EXCLUDED}; "
        f"scheduler reached {concurrency(rows['schedule']['value'])} concurrent and serialised "
        f"{EXPECTED_SERIALISED} on port {CONTENDED_PORT}"
    )
    return 0


if __name__ == "__main__":
    if sys.argv[1] == "--affected":
        sys.exit(main_affected(sys.argv[2], sys.argv[3]))
    sys.exit(main(sys.argv[1], sys.argv[2]))
