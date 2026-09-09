# Never interpolate an expression into a `run:` body

`${{ inputs.* }}` and `${{ steps.*.outputs.* }}` go into that step's `env:` block and are
referenced as shell variables (`"$DIR"`, never `"${{ inputs.dir }}"`). A `run:` body is the
only place text is spliced into something a shell then executes, so interpolation there is a
script-injection vector whatever the value looks like; `if:` conditions and `with:` blocks on
a `uses:` step are fine. Semgrep's `yaml.github-actions.security.run-shell-injection` has
caught this in a lydite action already.

Reasoning: [`.agents/references/actions.md`](../references/actions.md).
