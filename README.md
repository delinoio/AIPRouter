# Delino Base

Shared repository foundation for new Delino projects. No application or product projects are included.

The base retains development instructions, reusable agent skills, documentation templates, Apache-2.0 licensing, a pinned formatter, Git hooks and CI. Project implementations, project dependencies, installers, generated bindings, deployment configuration and release workflows are excluded.

## Setup

Use Node.js 24 and pnpm 10.26.2.

```sh
pnpm install --frozen-lockfile
pnpm test
pnpm format:check --all
```

## Add a project

Start with [the documentation catalog](docs/README.md) and [repository instructions](AGENTS.md). Create a project index and at least one domain contract before runtime implementation. Add language workspaces and build, test and release workflows when their first owning project is introduced.

## License

Repository-owned content is licensed under [Apache-2.0](LICENSE). Imported skills retain their original licenses and notices.
