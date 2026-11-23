# Installation

Cog is one program. Download it, run it, and setup takes care of the rest:
where it lives, the admin account, starting with your computer, and, if
you want it, reaching it from anywhere over HTTPS.

## Download

Get the file for your computer from the
[releases page](https://github.com/cog-app/cog/releases/latest):

| Computer | File |
|----------|------|
| Windows (most PCs) | `cog-windows-amd64.exe` |
| Mac with Apple silicon (M1 and later) | `cog-darwin-arm64` |
| Mac with an Intel processor | `cog-darwin-amd64` |
| Linux on a PC | `cog-linux-amd64` |
| Linux on a Raspberry Pi 4/5 or other 64-bit ARM | `cog-linux-arm64` |

## Run setup

### Windows

Double-click `cog-windows-amd64.exe`. Setup opens in your browser.

The first time, Windows SmartScreen may say it protected your PC from an
unrecognised app: choose **More info**, then **Run anyway**. When Cog first
listens on the network, Windows Firewall asks whether to allow it; allow it
on private networks, and on public ones too if you will forward ports to it.

### macOS

Double-click the file. macOS opens it in Terminal, and setup opens in your
browser.

Downloaded programs that are not from the App Store are blocked the first
time. Open **System Settings > Privacy & Security**, find the message about
`cog-darwin-arm64` near the bottom, and choose **Open Anyway**. Or, in
Terminal:

```bash
chmod +x ~/Downloads/cog-darwin-arm64
xattr -d com.apple.quarantine ~/Downloads/cog-darwin-arm64
~/Downloads/cog-darwin-arm64
```

### Linux

```bash
chmod +x cog-linux-amd64
./cog-linux-amd64 setup
```

With a desktop, setup opens in your browser; over SSH or on a server with no
desktop, it asks its questions in the terminal. `--cli` and `--web` choose
one or the other.

## What setup asks

Setup asks one question a page, and skips the pages your answers make
unnecessary. If you already know what you want, **Show every setting on
one page** on the first page puts them all on one form.

1. **How Cog runs.** *On this computer, for me* starts Cog when you sign
   in and runs it as you, with no administrator rights: the right choice
   for a home computer. *As a server* starts it with the computer, as an
   account of its own, and needs an administrator's password.
2. **Where to keep your files.** One folder holds Cog's program, settings
   and everyone's files. Pick a disk with room.
3. **Where you will use Cog from:**
   - *only on this computer*;
   - *at home, on your network*, over plain HTTP or over HTTPS with a
     certificate Cog makes (browsers warn once per device; setup shows the
     fingerprint to compare);
   - *anywhere, on your devices with Tailscale*, if Tailscale runs on this
     computer: no ports to open, and Tailscale Funnel if people without
     Tailscale need share links;
   - *anywhere, including people you share with*, over HTTPS with your
     router forwarding ports to you;
   - *through a reverse proxy you already run*.

   See [Reaching Cog from anywhere](../admin/https.md).
4. **SFTP and SSH**, and whether account passwords may sign in over it.
5. **Updates**: check daily, and whether to install them automatically.
6. **The admin account** you sign in with.

Setup then shows your answers, each with a link to change it, and what it
will do, including the port forwarding rules to add to your router. Then
it does it: copies Cog into the folder, writes its settings, makes the
admin account, gets a certificate, starts the service, and gives you the
link to open.

## Running setup again

Run the downloaded file again, or the installed one (`bin/cog` in the
folder you chose), to:

- change any setting: setup opens on a summary of the current ones, each
  with a link to the question that sets it;
- reset an admin's password, for when you are locked out;
- remove the service (your files and data stay in the folder).

## Containers and distribution packages

The container image is built without setup or the updater: you configure
it with [environment variables](configuration.md) and update it by pulling
a new image.

```bash
docker build -t cog .
docker run -p 8580:8580 -p 8522:8522 -v cog-data:/data cog
```

To build such a binary yourself, use the `nosetup` build tag:
`go build -tags nosetup ./cmd/cog`.

## From source

Requires Go 1.26 or later and a C compiler (for SQLite).

```bash
go install git.duckfam.us/jj-software/cog/cmd/cog@latest
cog setup
```

## Optional tools

Some features use other programs when they are installed. Run `cog doctor`
to see which are available.

| Tool | Used by | For |
|------|---------|-----|
| ffmpeg / ffprobe | Gallery, Feeds | Video thumbnails, metadata, media packaging |
| LibreOffice | Files | Document previews |
| tesseract | Search | Text in images |
| yt-dlp | Feeds | Video downloads |

Everything else is built into the one program.

## Next steps

- [First Run](first-run.md) -- sign in and look around
- [Configuration](configuration.md) -- the settings file and environment variables
