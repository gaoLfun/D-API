# Contributing to D-API

Thank you for helping improve D-API.

## Before You Start

- Search existing issues and pull requests first.
- Use a bug report for reproducible defects and a feature request for proposed changes.
- Do not open public issues for security vulnerabilities; follow [SECURITY.md](SECURITY.md).
- Deployment and environment-specific troubleshooting are outside the project's support scope.

For a substantial behavior or API change, open a feature request before writing code so the scope can be agreed first.

## Development

D-API officially supports development and deployment on Linux with Docker Compose. Other environments are best effort.

The backend requires Go 1.26 and PostgreSQL 17. The frontend requires Node.js 26.

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

Keep changes focused, preserve existing behavior outside the requested scope, and add a regression test for bug fixes when practical. Never commit credentials, `.env` files, database dumps, or production logs.

## Pull Requests

- Explain what changed and why.
- Link related issues.
- Describe how the change was tested.
- Update user-facing documentation when behavior or configuration changes.
- Keep unrelated refactors out of the pull request.

The project uses squash merging. Maintainers may ask for changes or close work that does not fit the project's lightweight, single-administrator scope.

No Contributor License Agreement or Developer Certificate of Origin sign-off is required. By submitting a contribution, you agree that it is licensed under the [Apache License 2.0](LICENSE).

All participation is subject to the [Code of Conduct](CODE_OF_CONDUCT.md).
