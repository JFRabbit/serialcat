# Security Policy

serialcat is an open-source CLI tool for streaming and filtering serial logs.
This document describes how to report security issues and what to expect.

## Reporting a vulnerability

If you find a security vulnerability in serialcat, please report it through one
of these channels — **do not open a public issue** for a vulnerability:

- **Preferred**: use GitHub private vulnerability reporting on this repository
  (go to the repository's **Security** tab → **Report a vulnerability**), or
- **Direct issue**: if private reporting is not available, open a regular issue
  on [the issue tracker](https://github.com/JFRabbit/serialcat/issues) that is
  clearly marked `[security]` in the title and avoid including exploit details
  in the first message; maintainers will reach out privately to continue.

Please include:

- The serialcat version and OS/platform affected.
- A minimal description of the vulnerability and how to reproduce it.
- The potential impact and, if known, a suggested fix.

## What to expect

- **Acknowledgment**: you will get an acknowledgment of your report within
  **5 business days**.
- **Investigation**: we will investigate and keep you informed of progress.
- **Fix & disclosure**: once a fix is ready, a patched release will be published
  and the vulnerability disclosed (with your consent, you will be credited).

## Supported versions

The project is a single-binary CLI where the latest release is always the
recommended one. Only the **latest release** is supported:

| Version | Supported |
|---|---|
| Latest release | ✅ |
| Older releases | ❌ |

Please upgrade to the latest release from the
[Releases page](https://github.com/JFRabbit/serialcat/releases) and keep your
binary up to date with `serialcat update`.
