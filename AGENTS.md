# Repository Guidance

This repository implements a Go plugin for CLIProxyAPI.
The root pnpm package owns contributor automation, not runtime provider code.
There is no maintained frontend, React application, or Gradle project.

## Development Lifecycle

Read the affected source, tests, and documented ABI before editing.
Stop discovery when evidence supports the smallest complete implementation.
Preserve the host callback boundary and host-owned credential storage.
Use existing translators and native tooling before adding another implementation or dependency.
Migrate callers and remove superseded interfaces in the same change.
Reproduce defects through observable behavior, then fix their cause.
Sequence coupled changes into units with explicit acceptance evidence.
Verify each unit before depending on it.
Reuse passing evidence only while relevant behavior, inputs, configuration, and toolchains remain unchanged.

Run `pnpm install --frozen-lockfile` to install tooling and Git hooks.
Run `pnpm run check` before handoff.
Run `pnpm run fix` for Go formatting and Markdown fixes, then inspect the diff and rerun checks.
Preserve `make test` as the Go test owner and `make package` as the production package owner.
Verify changed hook behavior through actual commits and pushes in disposable repositories.
Test failure propagation as well as successful execution.
Do not weaken checks or bypass hooks to publish failing changes.

Review the complete candidate diff and its callers before integration.
Report actual checks, valid reused evidence, and unrun acceptance separately.
Automated tests do not prove live provider, installed-host, or deployed-service behavior.
Remove task-owned temporary resources after recording evidence.
Preserve resources needed for recovery or continued work.

## Safety And Style

Keep credentials, callback URLs, local paths, and private environment details out of tracked content.
Use isolated checkouts for development involving deployment instructions.
Do not change running deployments or their configuration, plugins, or authentication assets during contributor-tooling work.
Keep references read-only.
Prefer officially supported LTS runtimes and compatible stable tools where no LTS line exists.
Retain compatible Go module, toolchain, ABI, and production image pins.
Write each complete documentation sentence on its own source line.
Do not join sentences with semicolons or em dashes.
Use compact Markdown tables and sorted configuration object keys.
Write actionable errors and recovery instructions for exposed interfaces.

## Reference Basis

The lifecycle rules adapt [pstack](https://github.com/cursor/plugins/tree/main/pstack/skills) principles for proof, sequencing, and root causes.
They align with the [Sinon Harness](https://github.com/ririnto/sinon/tree/main/plugins/harness) engineering and tooling baseline.
The [web interface guidelines](https://github.com/vercel-labs/web-interface-guidelines) inform actionable feedback where applicable.
React, Radix, Storybook, and frontend lint guidance apply only when a maintained frontend exists.
