---
about: pythonProducer runs `python3 -I` in the component directory; without -I the scanned tree's own modules or dist-info would be imported or trusted by a process holding lydite's environment
saw:
  - source/cli/internal/runner/runner.go
---

`pythonProducer` asks `python3` for the installed pytest and coverage.py versions through
`importlib.metadata`, in the component's directory so a directory-selected interpreter still resolves.
`python -c` puts the working directory first on `sys.path`, and `importlib.metadata` imports stdlib
modules lazily and scans `sys.path` for `*.dist-info`, so a tree carrying its own `email/` package
would run in a process with lydite's credentials and a hand-written `pytest-9.9.dist-info` would
spoof the version. `-I` removes the cwd, `PYTHON*` variables and user site-packages. The call still
inherits lydite's environment (`RunQuiet`), and a component's declared `env:` PATH is not honoured
because `Producer` takes no environment.
