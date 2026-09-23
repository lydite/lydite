#!/usr/bin/env python3
"""Assert no workflow in this repository uploads an artifact `publish` would fold.

`lydite/actions`' `publish` step downloads every artifact in the run matching
the glob `lydite-reports-*`, regardless of which job produced it. A workflow
here that names an `upload-artifact` step's `name:` with that prefix for any
other reason — a proving-ground fixture, a future job nobody thought to check
against this glob — folds into the repository's own verdict silently, the way
`ci-end2end.yml`'s proving-ground legs once did before they were renamed to
`proving-ground-reports-*` and renamed back only after download, inside the
job that produced them.

An artifact name can also come from the local `lydite-reports` composite
action (`.github/actions/lydite-reports`), whose `prefix` input defaults to
`lydite-reports` — a call site that never overrides it collides by default,
with no `upload-artifact` step of its own in the calling workflow at all.

This is intentionally not a YAML parser. A step is the text between one
`- uses:` (or `- name:`) list marker and the next at the same indentation;
`with.name` and `with.prefix` are the first `name:`/`prefix:` lines at deeper
indentation than the step's own marker. That is enough to tell an
`upload-artifact` (or `lydite-reports`) step's artifact name from an unrelated
`name:` key elsewhere in the file without pulling in a YAML library this check
doesn't otherwise need.

A name built from a `${{ … }}` expression is read only up to the point its
text is still fixed: `proving-ground-reports-nested-${{ matrix.slug }}-a`'s
fixed prefix is `proving-ground-reports-nested-`, decidable against the
colliding prefix without knowing what the matrix resolves to. A name whose
fixed prefix is a prefix of the colliding one in turn — `${{ inputs.name }}`
alone, or `lydite-${{ x }}` — cannot be decided either way from the text
alone, and is reported as a failure rather than silently passed: a check that
skips what it cannot evaluate can report success having examined nothing.
"""

import glob
import os
import re
import sys

WORKFLOWS_DIR = os.path.join(os.path.dirname(__file__), "workflows")
COLLIDING_PREFIX = "lydite-reports-"
LOCAL_REPORTS_ACTION = "./.github/actions/lydite-reports"
DEFAULT_ACTION_PREFIX = "lydite-reports"

STEP_MARKER = re.compile(r"^(?P<indent>[ \t]*)-\s")
USES_UPLOAD_ARTIFACT = re.compile(r"^\s*(?:-\s*)?uses:\s*actions/upload-artifact@")
USES_LOCAL_REPORTS_ACTION = re.compile(
    r"^\s*(?:-\s*)?uses:\s*" + re.escape(LOCAL_REPORTS_ACTION) + r"(?:@|\s|$)"
)
KEY_LINE = re.compile(
    r"^(?P<indent>[ \t]*)(?P<key>[A-Za-z_]+):\s*(?P<value>.*?)\s*(?:#.*)?$"
)


class Undecidable(Exception):
    """The step's name cannot be told apart from the colliding prefix by its text alone."""


def step_ranges(lines):
    """Yield (start, end) line-index ranges, one per top-level step in a `steps:` list."""
    starts = [i for i, line in enumerate(lines) if STEP_MARKER.match(line)]
    for pos, start in enumerate(starts):
        indent = STEP_MARKER.match(lines[start]).group("indent")
        end = len(lines)
        for later in starts[pos + 1 :]:
            later_indent = STEP_MARKER.match(lines[later]).group("indent")
            if len(later_indent) <= len(indent):
                end = later
                break
        yield start, end


def unquote(value):
    if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
        return value[1:-1]
    return value


def find_key(step, step_indent, key):
    """Return the unquoted value of the first `key:` line nested under the step, or None."""
    for line in step[1:]:
        match = KEY_LINE.match(line)
        if match and match.group("key") == key and len(match.group("indent")) > step_indent:
            return unquote(match.group("value"))
    return None


def fixed_prefix(value):
    """The leading run of `value` that is not inside a `${{ … }}` expression."""
    return value.split("${{", 1)[0]


def collides(name):
    """True if `name` names an artifact `publish`'s glob would fold in, raising
    Undecidable if the text does not fix that either way."""
    prefix = fixed_prefix(name)
    if prefix.startswith(COLLIDING_PREFIX):
        return True
    if "${{" in name and COLLIDING_PREFIX.startswith(prefix):
        raise Undecidable(name)
    return False


def uploads_named(path):
    """Return every artifact name this file's steps upload under."""
    with open(path, encoding="utf-8") as fh:
        lines = fh.readlines()
    results = []
    for start, end in step_ranges(lines):
        step = lines[start:end]
        step_indent = len(STEP_MARKER.match(step[0]).group("indent"))
        if any(USES_UPLOAD_ARTIFACT.match(line) for line in step):
            name = find_key(step, step_indent, "name")
        elif any(USES_LOCAL_REPORTS_ACTION.match(line) for line in step):
            prefix = find_key(step, step_indent, "prefix") or DEFAULT_ACTION_PREFIX
            suffix = find_key(step, step_indent, "name") or ""
            name = f"{prefix}-{suffix}"
        else:
            continue
        if name is None:
            continue
        results.append(name)
    return results


def main():
    failures = []
    patterns = [os.path.join(WORKFLOWS_DIR, "*.yml"), os.path.join(WORKFLOWS_DIR, "*.yaml")]
    for path in sorted({p for pattern in patterns for p in glob.glob(pattern)}):
        for name in uploads_named(path):
            try:
                collision = collides(name)
            except Undecidable:
                failures.append(
                    f"{os.path.relpath(path)}: uploads an artifact named {name!r}, whose fixed "
                    f"text neither matches nor rules out the glob `publish` folds into this "
                    f"repository's own verdict ({COLLIDING_PREFIX}*) — give it a prefix that "
                    "cannot resolve into one, so this check can tell without knowing what the "
                    "expression evaluates to"
                )
                continue
            if collision:
                failures.append(
                    f"{os.path.relpath(path)}: uploads an artifact named {name!r}, which "
                    f"matches the glob `publish` folds into this repository's own verdict "
                    f"({COLLIDING_PREFIX}*) — rename it (proving-ground-reports-* is the "
                    "existing convention for a fixture that must round-trip back to this "
                    "prefix only after its own job downloads it)"
                )
    for failure in failures:
        print(failure, file=sys.stderr)
    if failures:
        return 1
    print(f"no upload names an artifact matching {COLLIDING_PREFIX}*")
    return 0


if __name__ == "__main__":
    sys.exit(main())
