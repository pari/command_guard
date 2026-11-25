# Contributing to CommandGuard

Thanks for your interest. Bug reports, documentation fixes and pull requests are all welcome.

## Before you start

- For anything larger than a small fix, open an issue first so we can agree on the approach.
- Security problems go through the private process in [SECURITY.md](SECURITY.md), never a public issue.

## Building and running

```bash
./compile_and_run.sh
```

It builds `cguard` and `cg_sessiond` into `instance/`, refreshes the copy of `ui/` there, creates `instance/config.json` from the sample on the first run, and starts the server from inside `instance/`. All runtime data (config, keys, logs) lives in that git-ignored folder. UI changes need a rerun of the script, because `instance/ui/` is a copy.

## Tests

```bash
go vet ./...
go test ./...
```

Most tests are self-contained. Tests that execute real commands need SSH/WinRM test servers described in a local `test_env.json`; see [docs/test-setup.md](docs/test-setup.md). Never point them at production machines.

## Conventions

- **Never commit secrets.** `config.json`, `pem_files/` and `test_env.json` are git-ignored; keep it that way.
- **Config writes go through `mutateConfig`** in `config_api.go`. Reading the file in a handler and saving it afterwards loses concurrent edits.
- **Disable HTML escaping when encoding JSON** (`encoder.SetEscapeHTML(false)`), so `<`, `>` and `&` stay readable in `config.json`.
- **Bump the `?v=` query string** in `ui/index.html` / `ui/login.html` whenever you change a CSS or JS file.
- **Do not remove existing features or UI** as a side effect of another change.
- Add or update tests for behaviour you change, and update the relevant `*.md` document.

`CLAUDE.md` / `AGENTS.md` hold more detailed architecture notes; they are written for AI coding assistants but are a useful map for humans too.

## Licence

By contributing you agree that your contribution is licensed under the [MIT licence](LICENSE).
