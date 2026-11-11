---
description: "lydite's TypeScript front ends reuse vipengele/react and vipengele/typescript for UI, logging, resilience and library code before building their own."
---

# Reuse vipengele before building UI or library code

lydite's TypeScript front ends reuse the vipengele platform: `vipengele/react`
(<https://github.com/vipengele/react>, published as `@vipengele/react`, `@vipengele/react-ui`,
`@vipengele/react-tokens`, `@vipengele/react-telemetry`, `@vipengele/react-icons` and
`@vipengele/react-charts`) and `vipengele/typescript` (<https://github.com/vipengele/typescript>,
published as `@vipengele/ts` and the `@vipengele/ts-core-*` packages).

- The app mounts through `<Vipengele>` from `@vipengele/react`.
- Before creating any new React UI component, evaluate whether it belongs in `vipengele/react`
  instead. Only a component specific to lydite's domain lives in `source/web`, and the pull
  request description says why it is lydite-specific rather than leaving it there silently.
- Before creating any new library (non-UI) component, evaluate whether it belongs in
  `vipengele/typescript` instead.
- Logging uses `@vipengele/ts-core-observability/logger`, never `console` or another logging
  library.
- A resilient HTTP client uses `@vipengele/ts-core-resilience` — timeout, retry, circuit breaker
  and rate limiter — never a hand-rolled retry loop or another resilience library.

## Applies to

`source/web/**` and any future TypeScript front end or browser library in this repository.

## Example

```ts
// wrong: a hand-rolled retry loop and console logging
for (let i = 0; i < 3; i++) {
  try { return await fetch(url); } catch (e) { console.warn(e); }
}

// right: the platform's logger and resilience policies
import { Logging } from "@vipengele/ts-core-observability/logger";
import { createRetry, exponentialBackoff, withJitter } from "@vipengele/ts-core-resilience/retry";

const log = Logging.logger("dashboard.ledger");
const retry = createRetry("ledger.fetch", {
  maxAttempts: 4,
  backoff: withJitter(exponentialBackoff({ initialMs: 100, maxMs: 5_000 }), "full"),
});
const response = await retry.execute(({ signal }) => fetch(url, { signal }));
```

Reasoning: the repository owner's standing instruction; a component built here twice is a
component the platform's other consumers do not get.
