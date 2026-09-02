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

# The component is the coverage unit, and the proving ground is where that
# claim is falsifiable: `rust/` is one Cargo workspace of three crates and
# `web/` one npm workspace of two packages, so a run that still discovered its
# units by walking for manifests would report three Rust rows and two
# TypeScript ones. Only `go/api` and `go/sdk` are genuinely two.
#
# The assertion is the negative. "Every declared component has a coverage row"
# is satisfied by a run reporting five extra rows beside them, which is exactly
# the failure this exists to catch — so the count is pinned and any row naming
# a crate or package is a failure.
EXPECTED_COVERAGE_UNITS = ["tally", "api", "sdk", "web"]
# What `lydite scan` must run for each declared component, keyed by the language
# its runner implies. The row labels are `<tool>(<component>)`, so this asserts
# both that the check ran and that it ran for the right unit.
#
# The verdict is deliberately not asserted. The proving ground carries real
# findings on purpose — unlicensed crates, a WriteFile at 0644 — and a job that
# demanded green would have somebody tidy away the evidence a scanner works.
EXPECTED_SCAN_CHECKS = {
    "tally": ["cargo fmt", "cargo clippy", "cargo-audit", "cargo-deny"],
    "api": ["gosec", "govulncheck"],
    "sdk": ["gosec", "govulncheck"],
    "web": ["biome"],
}

# Names that appear in the tree as crates or packages and must never appear as
# coverage units: they are parts of a component, not components.
FORBIDDEN_COVERAGE_UNITS = [
    "tally-core",
    "tally-db",
    "tally-cli",
    "app",
    "shared",
]


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

    # Every component produces exactly one test(<name>) row whether it ran or
    # not, so what separates them is the status: a component that ran and
    # passed says 'pass', and one selection skipped says 'unmeasured'.
    for name in EXPECTED_AFFECTED:
        row = rows.get(f"test({name})")
        if row is None:
            failures.append(f"no test({name}) row, but a change under go/api/ must select {name}")
        elif row["status"] != "pass":
            failures.append(f"test({name}) is {row['status']!r}: {row.get('value', '')}")
    for name in EXPECTED_UNAFFECTED:
        row = rows.get(f"test({name})")
        if row is None:
            failures.append(
                f"no test({name}) row: a deselected component must be reported rather than "
                "dropped, or a reader cannot tell 'not affected' from 'not declared'"
            )
        elif row.get("status") != "unmeasured" or row.get("value") != "not affected":
            failures.append(
                f"test({name}) is {row!r}, want an unmeasured 'not affected' row — nothing in "
                "the change touches it, and a selection that returns every component satisfies "
                "every assertion about correctness while delivering none of the value"
            )

    # A component selection skipped is not measured, and its coverage row has
    # to say so rather than reporting a number. Naming the component that must
    # NOT have been measured is the coverage equivalent of naming the ones that
    # must not have run: a run that measured everything satisfies every
    # assertion about correctness and delivers none of what selecting is for.
    for name in EXPECTED_UNAFFECTED:
        row = rows.get(f"coverage({name})")
        if row is None:
            failures.append(
                f"no coverage({name}) row: a component that did not run must still be accounted "
                "for, or a narrowed run reads as a complete one"
            )
        elif row.get("status") != "unmeasured":
            failures.append(
                f"coverage({name}) is {row!r}, want unmeasured — nothing in the change touches "
                f"{name}, so there is no measurement it could honestly report"
            )
    for name in EXPECTED_AFFECTED:
        row = rows.get(f"coverage({name})")
        if row is None:
            failures.append(f"no coverage({name}) row, but {name} ran")

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

    # The component is the coverage unit. A run that still discovered units by
    # walking for manifests reports one row per crate and per package, so the
    # count is pinned and the crate and package names are forbidden outright —
    # asserting only that each component has a row would pass on a report
    # carrying five more beside them.
    units = sorted(
        label[len("coverage(") : -1]
        for label in rows
        if label.startswith("coverage(") and label.endswith(")")
    )
    if units != sorted(EXPECTED_COVERAGE_UNITS):
        failures.append(
            f"coverage units are {units}, want {sorted(EXPECTED_COVERAGE_UNITS)} — rust/ is one "
            "workspace of three crates and web/ one workspace of two packages, so each must be a "
            "single unit"
        )
    for forbidden in FORBIDDEN_COVERAGE_UNITS:
        if forbidden in units:
            failures.append(
                f"{forbidden} is reported as a coverage unit, but it is a crate or package inside "
                "a component rather than a component"
            )

    # A run that measured without gating must not render as one that gated.
    # This job passes no --gate-coverage, so every coverage row here has to
    # carry the status that says nothing was compared — a workflow that forgot
    # the flag otherwise reports exactly the green a gated run reports.
    for label in ("coverage",) + tuple(f"coverage({n})" for n in EXPECTED_COVERAGE_UNITS):
        row = rows.get(label)
        if row is None:
            failures.append(f"no {label} row")
        elif row.get("status") == "pass":
            failures.append(
                f"{label} is a pass, but this run gated nothing — a measured-but-ungated figure "
                "must be visibly distinct from one held to a baseline"
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
        f"{EXPECTED_SERIALISED} on port {CONTENDED_PORT}; coverage measured {units} and gated none "
        "of them"
    )
    return 0


def main_scan(path: str) -> int:
    """Assert `lydite scan` covered every declared component's language.

    Deliberately not the verdict. The proving ground is awkward on purpose —
    unlicensed crates, a WriteFile at 0644 — so real findings are the expected
    state, and asserting green would make somebody tidy the evidence away. What
    must hold is that every declared component was actually scanned by the
    checks its language implies: a run that scanned nothing exits 0 too.

    The row labels carry the component name, so this is also where a scan that
    silently narrowed to one component would show up.
    """
    with open(path, encoding="utf-8") as f:
        report = json.load(f)
    labels = [row.get("label", "") for row in report.get("rows", [])]

    failures = []
    for name, tools in EXPECTED_SCAN_CHECKS.items():
        for tool in tools:
            if f"{tool}({name})" not in labels:
                failures.append(f"no {tool} row for component {name}; rows were {labels}")
    if "semgrep" not in labels:
        failures.append(f"semgrep did not run; rows were {labels}")
    # A Cargo workspace is one component and one cargo invocation. A run that
    # had gone back to walking for manifests would report a row per member
    # crate, which is a name no component has.
    for forbidden in FORBIDDEN_COVERAGE_UNITS:
        if any(label.endswith(f"({forbidden})") for label in labels):
            failures.append(f"{forbidden} was scanned as a unit; it is part of a component, not one")

    for f in failures:
        print(f"proving ground scan: {f}", file=sys.stderr)
    if failures:
        return 1
    print(
        f"proving ground scan: every check ran for {sorted(EXPECTED_SCAN_CHECKS)} "
        "and semgrep ran once over the root"
    )
    return 0


if __name__ == "__main__":
    if sys.argv[1] == "--affected":
        sys.exit(main_affected(sys.argv[2], sys.argv[3]))
    if sys.argv[1] == "--scan":
        sys.exit(main_scan(sys.argv[2]))
    sys.exit(main(sys.argv[1], sys.argv[2]))
