# Never print a declared environment variable's value, only its name

A component's `env:` declaration is arbitrary text the repository controls, and the places a
repository puts a token are exactly the places that look like configuration: a registry URL with
credentials in it, a `*_TOKEN` a suite needs, a DSN. A warning or report line that reaches a CI
log — world-readable on a public repository — or a pull-request comment must never carry that
value. Redacting rather than omitting is the same failure as an allowlist: a pattern list that
must be complete to be safe publishes the secret it did not recognise while reading as though it
had checked. A name is enough — what a reader needs is that the variable was set for this
component; the value is in `.lydite/components.yml`, in the repository, under review.

## Applies to

`cmd/lydite/scan.go`'s `warnDeclaredEnv`/`declaredEnvNames`, and any future code that reports on a
component's declared or composed environment.

## Example

```go
// wrong: the value reaches a log a reviewer did not open the file to see
fmt.Fprintf(w, "warning: %s declares GOVULNDB=%s\n", c.Name, v)

// right: names only
fmt.Fprintf(w, "warning: %s's checks are composed with the environment %s declares: %s\n",
    c.Name, component.FileName, strings.Join(names, ", "))
```

Reasoning: [`docs/adr/0046-a-components-declared-environment-is-named-in-the-scan.md`](../../docs/adr/0046-a-components-declared-environment-is-named-in-the-scan.md)
and [`agentic/references/scanning.md`](../references/scanning.md).
