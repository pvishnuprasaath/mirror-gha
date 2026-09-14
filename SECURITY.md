# Security Policy

## Reporting a Vulnerability

If you find a security issue in `mirror-gha`, please report it privately
rather than opening a public issue — this tool executes untrusted
workflow/action code inside Docker containers on the reporter's own
machine, and issues here can have real local-execution implications.

Open a [GitHub Security Advisory](../../security/advisories/new) on this
repository, or email the maintainer directly if that's unavailable. Please
include:

- A description of the issue and its impact
- Steps to reproduce (a minimal workflow file, if applicable)
- Any suggested remediation

We'll acknowledge reports within a few days and aim to ship a fix before
any public disclosure.

## Supported Versions

Pre-1.0: only the latest commit on `main` is supported. There is no
versioned release/patch policy yet.
