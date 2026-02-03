# Agent Instructions

## Building

This project targets macOS. When verifying builds, cross-compile for darwin:

```sh
GOOS=darwin GOARCH=arm64 go build ./...
```

The `container` package uses macOS-specific APIs and will not build on Linux without this.

## Configuration System

When adding new configuration fields, update all of these locations:

1. **`config/config.go`** — Add struct fields, update `Merge()`, `SourceInfo`, `NewSourceInfo()`, and `trackConfigSources()`
2. **`silo.schema.json`** — Add JSON schema definition for editor autocompletion/validation
3. **`silo.jsonc.example`** — Add commented example
4. **`main.go`** — Update `sampleConfig` constant (used by `config init`)
5. **`main.go`** — Update `runConfigShow()` to display the new fields with source annotations
6. **`main.go`** — Update `runConfigDefault()` to display defaults
7. **`README.md`** — Document the feature in the user manual

### Config Types

- **Global config**: `Config` struct — applies to all runs
- **Tool config**: `ToolConfig` struct — applies when a specific tool is selected (keyed by tool name)
- **Repo config**: `RepoConfig` struct — applies when git remote URL contains the key as a substring

