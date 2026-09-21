# Contributing to AnchorShell Relay

Keep changes focused. Bug fixes, tests, and documentation improvements are welcome.
For larger changes, [contact AnchorShell](https://anchorshell.com/contact) first
to agree on scope. Read [LICENSE](LICENSE) before extending or redistributing Relay;
the Community Edition and hosted features have different licensing boundaries.

## Local setup

Use Git, Make, Go 1.26.4 or a newer Go 1.26 patch, and Node.js 22.19.0 with npm.
The Node version is pinned in `.nvmrc`.

From the repository root:

```bash
make deps
make install-dev-tools
make dev
```

The development dashboard is at `http://localhost:3030` and the backend is at
`http://localhost:11730`. Stop an existing Relay process before starting another.
The [README](README.md#quick-start) covers generated credentials and first use.

## Checks

Run backend checks from the repository root:

```bash
go test ./...
go vet ./...
```

Run frontend tests with the pinned Node version:

```bash
cd web
npm test
```

For frontend or embedded-dashboard changes, verify the complete build from the
repository root:

```bash
make build
```

Use `gofmt` on changed Go files. Add a regression test for a bug fix.
Tests must use fixtures, not real provider credentials or paid inference calls.

## Pull requests

- Explain the problem, the scope, and the checks you ran.
- Keep unrelated refactors and dependency updates out of the change.
- Preserve existing API contracts and the standalone runtime boundary.
- Include before/after images for visible UI changes, without secrets or personal data.
- Never commit `.env`, local databases, credentials, logs, or generated build output.

[Architecture](docs/ARCHITECTURE.md) and [agent working notes](AGENTS.md) describe
the implementation boundaries. Report vulnerabilities through the
[security reporting guide](https://anchorshell.com/security), not a public issue.

<!-- TODO BEFORE ACCEPTING EXTERNAL PRs: Publish the approved contribution/CLA process. No CLA is currently specified. -->
