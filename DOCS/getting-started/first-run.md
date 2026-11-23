# First Run

## Sign in

When setup finishes it shows **Open Cog**. Sign in with the admin account
you made. The dashboard shows an app for each enabled plugin.

If Cog is reachable from the internet, do these first:

1. **Settings > Security**: turn on two-factor sign-in.
2. **Settings > Security**: add an SSH key if you use SFTP or rsync, and
   app passwords for the Nextcloud desktop and phone apps.
3. **Admin > Users**: add the people who will use Cog.

## Where things are

Everything is in the folder you chose in setup:

```
<folder>/
├── bin/cog         the program (the updater replaces it)
├── cog.toml        settings
├── system.db       accounts, sessions, shares, settings
└── data/           everyone's files and app data
```

Back up the whole folder. See [Backup and Restore](../admin/backup.md).

## Stopping and starting

Setup installed a service, so Cog starts by itself. To stop or restart it:

| | For me | System service |
|---|---|---|
| Linux | `systemctl --user restart cog` | `sudo systemctl restart cog` |
| macOS | `launchctl kickstart -k gui/$(id -u)/us.duckfam.cog` | `sudo launchctl kickstart -k system/us.duckfam.cog` |
| Windows | Task Scheduler, task *Cog* | Services, *Cog* |

Its log is in `journalctl --user -u cog` (or `-u cog` for the system
service) on Linux, `cog.log` in the folder on macOS, and Event Viewer on
Windows. **Admin > Logs** shows recent entries on every platform.

## The command line

The installed program finds its settings on its own, so admin commands
work from the folder's `bin`:

```bash
~/.local/share/cog/bin/cog user list
~/.local/share/cog/bin/cog doctor
```

For a system service, run them as its account so files it makes belong to
the server: `sudo -u cog /opt/cog/bin/cog user list`.

## Without setup

To run Cog in the foreground with no service, in the current folder:

```bash
cog user create --admin alice <password>
cog serve
```

It listens on `:8580` (HTTP) and `:8522` (SSH), keeps `system.db` and
`data/` in the current folder, and is configured with
[environment variables](configuration.md).

## Enable or disable apps

**Admin > Plugins** turns apps on and off. Dashboard and Files are always
on.

## Next steps

- [Reaching Cog from anywhere](../admin/https.md)
- [Updates](../admin/updates.md)
- [CLI Reference](../admin/cli.md)
- [Email Setup](../admin/email.md) -- for password resets and email codes
