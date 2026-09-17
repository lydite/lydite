# What each runner's JUnit report actually looks like

These are captured reports, not written ones. A hand-written JUnit is evidence for its author's
reading of the format, which is the one thing it must not be evidence for —
[ADR 0041](../../../../../docs/adr/0041-a-new-test-is-rerun-in-rust-and-typescript-too.md) rests
on what `classname` and `name` hold in each, and the answer differs per runner.

The probes these were taken over live under
[`../../flaky/testdata/`](../../flaky/testdata/): `nextestprobe/` for the Rust captures,
`vitestprobe/` for the TypeScript ones.

## `nextest-suite.xml`, `nextest-rerun.xml`

cargo-nextest 0.9.96 over `flaky/testdata/nextestprobe/`, whose one crate carries four test
binaries. `classname` is the binary — `nextestprobe` for the lib's own `#[cfg(test)]` module,
`nextestprobe::a`, `::b`, `::c` for each file under `tests/`. `name` is the test's path within
that binary, enclosing `mod` names joined with `::`: `tests::nested::doubles_deeper`,
`inner::only_in_a`, `async_cases::awaits_and_agrees`.

`shared_name` appears twice, under `nextestprobe::a` and `nextestprobe::b`. That is the collision
`name` alone cannot resolve, and the reason the rerun path keys on `classname` plus `name`.

`ignored_by_attribute`, which carries `#[ignore]`, is in neither report at all — nextest omits it
rather than recording it as skipped, so an ignored test is a test run 1's report does not record.

`nextest-rerun.xml` is the second run, `--profile rerun` with an OR-ed exact filter:

```sh
cargo nextest run --tool-config-file "lydite:<cache>/lydite.toml" --profile rerun --no-fail-fast \
  -E 'test(=shared_name) or test(=tests::nested::doubles_deeper) or test(=async_cases::awaits_and_agrees)'
```

It holds four test cases and no others: nextest drops the filtered-out tests from the report
instead of marking them skipped. `test(=shared_name)` selected the test in both binaries, because
an exact-name filter names a test and not a binary.

## `vitest-workspace-suite.xml`

vitest 5.0.0 over `source/cloud-services` unmodified, `--reporter=junit`. `classname` is the test
file's path relative to the workspace root; `name` is the enclosing `describe` titles and the
test's own title joined with `" > "` — `appJwt &gt; backdates iat so a clock a second fast is not
a rejection`. The separator is a literal space-greater-space, XML-escaped, and it is what a
statically enumerated TypeScript test name has to reproduce to match a row in this report.

## `vitest-probe-suite.xml`, `vitest-probe-rerun.xml`, `vitest-probe-rerun-unescaped.xml`

The same vitest over `flaky/testdata/vitestprobe/`, which holds the shapes the workspace does not.
`shared title` appears in both `one.test.ts` and `two.test.ts` under one `classname` each — the
TypeScript collision. `outer &gt; inner &gt; holds a title two describes deep` is the two-level
chain. `doubles 1 into 2` and `doubles 2 into 4` are one `test.each`, and `a title built from a
computed expression` is a template literal: both names exist only after the file has run.

The two rerun captures are the same invocation with one difference:

```sh
vitest run libs/probe/src/one.test.ts libs/probe/src/two.test.ts \
  -t '^(matches \^a \(b\) \[c\] \+ d\$|shared title|outer > inner > holds a title two describes deep)$'
```

`-t` is a regular expression. In `vitest-probe-rerun-unescaped.xml`, taken with the same
alternation and no backslashes, `matches ^a (b) [c] + d$` is `<skipped/>` — its own title did not
match the pattern built from it. Under the gate's rule that a skip is an outcome, an unescaped
pattern reports a perfectly deterministic test as flaky.

Both rerun captures also show what vitest does that nextest does not: every test in a named file
is in the report, and the ones `-t` excluded carry `<skipped/>`. Absence from a vitest rerun means
the file was not named; a skip means either the test skipped itself or the pattern missed it.
