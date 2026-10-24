# Security CI

The `security` workflow scans every locked npm dependency, including development/build dependencies, and every required Go module. It also builds the existing `web` and `backend` Dockerfiles and scans their final runtime images. The policy blocks HIGH/CRITICAL vulnerabilities with a published fix; unfixed and lower-severity vulnerabilities remain outside this workflow's policy.

Trivy is pinned by digest. A scanner failure, an error in scanner diagnostics, malformed/empty JSON, missing expected package versions, or a policy finding fails the job. Source inventory expectations come from `web/package-lock.json` and `backend/go.mod`. Runtime expectations come from the actual exported image filesystem: the installed Alpine database, installed npm package metadata and Node version for web, and Go binary build information including its standard library and linked modules for the API. The inventory check compares every expected name/version against the scan, rather than treating an empty vulnerability list as proof of coverage.

Reports, expected inventories and scanner diagnostics are uploaded for seven days, including when a scan fails. The workflow runs for pull requests, pushes to `main`, manual dispatch and daily at 05:23 UTC. Scheduled runs operate from the default branch, so the schedule starts after the workflow reaches that branch. Failed-run email depends on the account's Actions notification preferences.

The web build and runtime use Alpine 3.22 with Node 22. The runtime installs Alpine's `nodejs` package so the scanner can inventory Node itself; the upstream `node:22-alpine` tarball is absent from the APK package database. The gate also checks that the running Node version agrees with its installed package record. The standalone server command, port and unprivileged user stay the same.

The isolated web smoke check validates the standalone entrypoint, loads the native image module and renders `/login` with networking disabled. The root redirects to authenticated `/lessons`, which requires the API, so it is not used as a self-contained web image check.

Existing accepted risks, when present, use `.trivyignore.yaml`; this change adds no exclusions. To change a manifest format, Go replacement rules, runtime package manager or Dockerfile layout, update the inventory implementation and its regression controls together. Run the controls locally with `python3 -m unittest discover -s scripts -p 'test_*.py'`. Real image validation requires Docker; the API inventory also requires Go to read build metadata.
