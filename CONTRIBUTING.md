# Contributing to fastr

Thanks for considering a contribution. The project is in its early specification phase, so this
is the best moment to influence what it becomes.

## Before you write code

fastr is governed by a [constitution](.specify/memory/constitution.md). It is short and it is
binding: a pull request that violates a principle will be rejected regardless of code quality.
Read it first. The parts that surprise people most often:

- nothing may reach out to the internet at runtime, including for fonts and icons
- the mobile side is a browser page, never an installed app
- no file size cap may be hard-coded, and files are never buffered whole in memory
- every feature must work identically on Linux and on Windows

## Workflow

The project uses [GitHub Spec Kit](https://github.com/github/spec-kit): specifications are written
before implementation and live in the repository under `.specify/` and `specs/`.

For anything larger than a typo:

1. **Open an issue first.** Describe the problem before the solution. This avoids work that will
   not be merged.
2. **Agree on the specification.** Non-trivial changes get a spec before code. If you use an
   agent with Spec Kit installed, `/speckit-specify` then `/speckit-plan` then `/speckit-tasks`
   is the intended path. If you do not, plain prose in the issue is fine.
3. **Implement on a branch** cut from `main`.
4. **Open a pull request** referencing the issue.

Small fixes (typos, broken links, obvious one-line bugs) can go straight to a pull request.

## Quality gates

A pull request must clear all of these before it can merge:

1. Unit and integration tests green on **both** Linux and Windows.
2. End-to-end transfer tests: small file, large file, resume after a network drop, integrity
   verification.
3. Network regression test: no outbound request beyond the local network.
4. Security tests: unpaired access rejected, path traversal rejected, secrets absent from logs.
5. No throughput regression above 10% on the reference scenario.
6. Secret scanning clean.

Any new dependency, service, or abstraction layer needs a written justification in the pull
request. When two designs are equally correct, the simpler one wins.

## Conventions

- **English everywhere** in the repository: code, comments, documentation, commit messages,
  issues, and pull requests. Discussion in other languages is welcome in issues, but the artifacts
  that ship stay in English.
- **Commit messages** follow [Conventional Commits](https://www.conventionalcommits.org/):
  `feat:`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`.
- **Never commit** a key, a certificate, a pairing token, a personal filesystem path, or a local
  IP address. Generated credentials belong to runtime state.
- **Test fixtures** stay small. Large files used in transfer tests are generated locally, never
  committed.

## Reporting a bug

Open an issue with the bug report template. The details that actually help:

- your operating system and version, on both ends
- the mobile browser and version
- the file size, and whether the transfer started at all
- what you expected, and what happened instead

Never paste a pairing code, a certificate, or a token into an issue.

## Reporting a vulnerability

Do not open a public issue. Follow [SECURITY.md](SECURITY.md).

## Code of conduct

Participation is covered by the [Code of Conduct](CODE_OF_CONDUCT.md). Be decent to people.

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE).
