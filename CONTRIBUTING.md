# Contributing to D-API

Thank you for helping improve D-API. The project is deliberately small and
focused; a clear bug report, regression test, or precise documentation fix is
more useful than a broad refactor.

## Before You Start

- Search existing issues and pull requests first.
- Use a bug report for reproducible defects and a feature request for proposed changes.
- Do not open public issues for security vulnerabilities; follow [SECURITY.md](SECURITY.md).
- Deployment and environment-specific troubleshooting are outside the project's support scope.
- Read [SUPPORT.md](SUPPORT.md) and [docs/README.md](docs/README.md) before opening an issue.

For a substantial behavior or API change, open a feature request before writing code so the scope can be agreed first.

## Development

D-API officially supports development and deployment on Linux with Docker Compose. Other environments are best effort.

The backend requires Go 1.26 and PostgreSQL 17. The frontend requires Node.js 26.

The repository contains both English and Simplified Chinese user documentation.
Behavior or configuration changes should update both README files and the
relevant document under `docs/`.

Run the relevant checks before submitting a pull request:

```sh
go test ./...
go vet ./...
go test -race ./...

cd web
npm ci
npm test
npm run build
npm audit --audit-level=high
```

When Go or PostgreSQL are unavailable locally, run the backend checks with the
same container images used by CI. PostgreSQL integration tests run only when
`DAPI_TEST_DATABASE_URL` is set.

Keep changes focused, preserve existing behavior outside the requested scope, and add a regression test for bug fixes when practical. Never commit credentials, `.env` files, database dumps, or production logs.

Use the existing standard library, SQL, and Vue patterns before adding a
dependency. New dependencies require a clear runtime or maintenance benefit and
an entry in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Pull Requests

- Explain what changed and why.
- Link related issues.
- Describe how the change was tested.
- Update user-facing documentation when behavior or configuration changes.
- Keep unrelated refactors out of the pull request.
- For security fixes, do not include an exploit or secret in a public PR; follow
  [SECURITY.md](SECURITY.md) and coordinate disclosure privately.
- For user-facing changes, include the exact UI/API behavior and update the
  changelog when the change is release-relevant.

The project uses squash merging. Maintainers may ask for changes or close work that does not fit the project's lightweight, single-administrator scope.

No Contributor License Agreement or Developer Certificate of Origin sign-off is required. By submitting a contribution, you agree that it is licensed under the [Apache License 2.0](LICENSE).

Commit messages are not enforced, but concise Conventional Commit-style subjects
(`fix:`, `feat:`, `docs:`, `security:`) make the changelog and release review
easier. Maintainers squash-merge accepted pull requests.

All participation is subject to the [Code of Conduct](CODE_OF_CONDUCT.md).
