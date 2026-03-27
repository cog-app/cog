# First Run

## Create an admin user

Before starting the server, create an initial admin account:

```bash
cog user create --admin alice
```

You will be prompted for a password.

## Start the server

```bash
cog serve
```

Or simply:

```bash
cog
```

Running `cog` without a subcommand starts the HTTP server. By default it listens on `:8580` and enables the SSH/SFTP server on `:8522`.

## Log in

Open `http://localhost:8580` in your browser and log in with the user you created. The dashboard shows the app grid with all enabled plugins.

## What happens on first start

1. The system database (`system.db`) is created if it doesn't exist.
2. Database migrations run automatically.
3. A CSRF secret is generated and stored.
4. All built-in plugins are enabled by default.
5. Per-user directories are created on first login.

## Enable or disable plugins

Visit **Admin > Plugins** in the web UI to enable or disable apps. The Dashboard and Files plugins are protected and cannot be disabled.

## Next steps

- [CLI Reference](../admin/cli.md) -- manage users, groups, and volumes from the command line
- [Email Setup](../admin/email.md) -- configure SMTP for password resets and email OTP
- [Reverse Proxy](../admin/reverse-proxy.md) -- put Cog behind nginx or Caddy
