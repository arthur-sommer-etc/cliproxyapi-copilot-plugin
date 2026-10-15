# Repository Guidance

This repository implements a Go plugin for CLIProxyAPI.
The root pnpm package owns contributor automation, not runtime provider code.
There is no maintained frontend, React application, or Gradle project.

## Contributor Tooling

Keep Node dependency ranges in the `pnpm-workspace.yaml` catalog as `^x.y.z` and reference them with `catalog:` in `package.json`.
Keep the pnpm version in `packageManager` without a version-plus-hash pin.
Preserve lockfile integrity fields and compatible Go module and toolchain versions.
Prefer supported LTS runtimes and compatible stable tools where no LTS line exists.

Run `pnpm install --frozen-lockfile` to install tooling and Git hooks.
Keep the `prepare` script running `husky` and the real hooks running `pnpm run pre-commit` and `pnpm run pre-push`.
Run `pnpm run check` before handoff.
Run `pnpm run fix` for Go formatting and Markdown fixes, then inspect the diff and rerun checks.
Preserve `make test` as the Go test owner and `make package` as the production package owner.
Verify hook changes through successful and rejected commits and pushes in disposable repositories.
Ultracite is the only caller of `oxlint` and `oxfmt` when Node source linting or formatting applies.

Public CI runs on pull requests, manual dispatch and tag pushes.
Keep release publication restricted to release tags.
Keep Markdownlint configuration keys sorted, with `line-length: false`, compact `table-column-style` and `gitignore: true`.
Add an explicit Markdown ignore only for a concrete file Git ignore cannot cover.

## Source And Documentation

Assert error identity, type or structured fields in tests rather than exact human-readable prose.
At string-only host callbacks, assert terminal success or failure and structured callback arguments and ordering.
Add documentation comments only, preserving required language directives and license notices.
Treat condition inversion and flat `if` / `else if` / `else` chains as guidance for trivially equivalent local changes.
Lint rules and formatter output take precedence.
Keep required error, cancellation, overflow and admission exits, preserving evaluation order, side effects, cost and cleanup.
Avoid broad exit sweeps, deeper nesting, artificial flags and lint suppressions.
Write one complete documentation sentence per line without semicolon joins or em dashes.

## Plugin Lifecycle

Preserve the host callback ABI, host-owned credential storage and existing translators.
Verify real callback outcomes and cleanup order through the existing fixtures before depending on a change.
Source and fixture checks do not establish live provider, installed-host or deployed-service acceptance.
Keep broader provider and original-client acceptance tracked separately from contributor tooling.

## Reference Basis

The lifecycle rules adapt [pstack](https://github.com/cursor/plugins/tree/main/pstack/skills) principles for proof, sequencing, and root causes.
They align with the [Sinon Harness](https://github.com/ririnto/sinon/tree/main/plugins/harness) engineering and tooling baseline.
