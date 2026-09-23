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

This is intentionally not a YAML parser. A step is the text between one
`- uses:` (or `- name:`) list marker and the next at the same indentation, and
`with.name` is the first `name:` line at deeper indentation than the step's
own marker. That is enough to tell an `upload-artifact` step's artifact name
from an unrelated `name:` key elsewhere in the file without pulling in a YAML
library this check doesn't otherwise need.
"""

import glob
import os
import re
import sys

WORKFLOWS_DIR = os.path.join(os.path.dirname(__file__), "workflows")
COLLIDING_PREFIX = "lydite-reports-"

STEP_MARKER = re.compile(r"^(?P<indent>[ \t]*)-\s")
USES_UPLOAD_ARTIFACT = re.compile(r"^\s*(?:-\s*)?uses:\s*actions/upload-artifact@")
NAME_KEY = re.compile(r"^(?P<indent>[ \t]*)name:\s*[\"']?(?P<value>\S+?)[\"']?\s*(?:#.*)?$")


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


def uploads_named(path):
    """Return every `name:` value an `upload-artifact` step in `path` uploads under."""
    with open(path, encoding="utf-8") as fh:
        lines = fh.readlines()
    names = []
    for start, end in step_ranges(lines):
        step = lines[start:end]
        if not any(USES_UPLOAD_ARTIFACT.match(line) for line in step):
            continue
        step_indent = len(STEP_MARKER.match(step[0]).group("indent"))
        for line in step[1:]:
            match = NAME_KEY.match(line)
            if match and len(match.group("indent")) > step_indent:
                names.append(match.group("value"))
                break
    return names


def main():
    failures = []
    for path in sorted(glob.glob(os.path.join(WORKFLOWS_DIR, "*.yml"))):
        for name in uploads_named(path):
            if name.startswith(COLLIDING_PREFIX):
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
    print(f"no upload-artifact step names an artifact matching {COLLIDING_PREFIX}*")
    return 0


if __name__ == "__main__":
    sys.exit(main())
