# Installation

## From source

Requires Go 1.25 or later.

```bash
go install git.duckfam.us/jj-software/cog/cmd/cog@latest
```

## Build from a clone

```bash
git clone https://git.duckfam.us/jj-software/cog.git
cd cog
go install ./cmd/cog
```

## Optional dependencies

Some features require external tools. Run `cog doctor` after installation to check which are available.

| Tool | Required by | Purpose |
|------|-------------|---------|
| ffmpeg / ffprobe | Gallery | Video thumbnails and metadata extraction |

All other functionality is self-contained in the Go binary.

## Verify installation

```bash
cog doctor
```

This checks that the binary is working and reports the status of optional dependencies.

## Next steps

- [Configuration](configuration.md) -- environment variables and directory layout
- [First Run](first-run.md) -- create your first user and start the server
