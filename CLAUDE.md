# Agent Instructions

## Building

This project targets macOS. When verifying builds, cross-compile for darwin:

```sh
GOOS=darwin GOARCH=arm64 go build ./...
```

The `container` package uses macOS-specific APIs and will not build on Linux without this.
