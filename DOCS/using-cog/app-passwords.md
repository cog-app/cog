# App Passwords

App passwords are alternative credentials for use with third-party clients and scripts. They work anywhere your main password works (WebDAV, SFTP, API) but can be individually revoked without changing your main password.

## Why use app passwords

- **Security** -- if a client is compromised, revoke its app password without affecting other clients
- **Convenience** -- some clients (NextCloud desktop/mobile) create app passwords automatically via Login Flow
- **Automation** -- use app passwords in scripts and cron jobs

## Creating an app password

1. Go to **Settings > Security**
2. Under "App Passwords", enter a name for the new password (e.g. "laptop sync", "backup script")
3. Click **Create**
4. Copy the generated password -- it is shown only once

## Using app passwords

App passwords work with:

- **WebDAV** -- use as the password in Basic Auth or Bearer token
- **SFTP/SSH** -- use as the password when connecting
- **API** -- use as a Bearer token in the `Authorization` header
- **NextCloud clients** -- Login Flow creates app passwords automatically

## Revoking app passwords

Go to **Settings > Security** and click the delete button next to the app password you want to revoke. The client using that password will immediately lose access.
