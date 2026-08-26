# Security Policy

## Supported Versions

Security fixes are applied to `main`. The latest published release is the only
version expected to receive backports; older tags should be upgraded after a
fix is announced. v0.x releases may include configuration or management API
changes.

## Deployment Boundary

D-API is designed for one trusted administrator on a single host. Protect the
admin console with HTTPS, a firewall or private network, and a strong unique
password. Do not use the direct `DAPI_BIND` port as a production substitute for
TLS. The gateway blocks private, loopback, link-local, multicast, CGNAT, and
cloud metadata destinations, but this does not replace an egress firewall.

The `DAPI_MASTER_KEY` encrypts upstream, notification, and client-key copies.
It is intentionally not stored in PostgreSQL. Treat it like a root secret and
back it up separately. Request and response bodies are not persisted, but URLs,
models, client IPs, timings, status codes, and token metadata can appear in
operational records.

## Reporting a Vulnerability

Do not disclose suspected vulnerabilities in a public issue, discussion, or pull request.

Use GitHub's [private vulnerability reporting](https://github.com/gaoLfun/D-API/security/advisories/new) to send the maintainers:

- the affected version or commit;
- reproduction steps or a proof of concept;
- the expected impact; and
- any suggested mitigation, if available.

Remove API keys, passwords, personal data, and unrelated production data from reports. The maintainers will respond through the private advisory, validate the report, coordinate a fix when needed, and credit reporters who request attribution. Please allow time for a fix before public disclosure.

If private vulnerability reporting is unavailable, contact the repository
maintainer through the email address listed in the GitHub profile. Do not create
a public issue as a fallback.

Include whether the issue affects the gateway, admin API, outbound probes, or
deployment files. A safe proof of concept, affected commit, and mitigation
suggestion are useful; do not include live endpoints or credentials.

This project does not offer a bug bounty, guaranteed response time, or paid
support. Disclosure timing is coordinated case by case after a fix is available.
