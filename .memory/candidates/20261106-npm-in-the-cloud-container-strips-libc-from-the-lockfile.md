---
about: "npm install with the cloud container's npm 10.9.9 strips every libc field from source/cloud-services/package-lock.json"
saw:
  - source/cloud-services/package-lock.json
---
- Running `npm install` in `source/cloud-services` with npm 10.9.9 (the cloud container's) rewrote the lockfile to drop every `"libc": ["glibc"]` / `["musl"]` field on the optional native packages (`@img/sharp-*`, `lightningcss-*`) — ~80 lines of churn unrelated to the dependency being changed. The committed lockfile carries those fields from a newer npm, and losing them weakens platform-specific binary selection.
- A dependency edit there is better made as a targeted edit to the one workspace entry in `package-lock.json`, then confirmed with `npm ci` (which does not rewrite the lock and fails if it is out of sync).
