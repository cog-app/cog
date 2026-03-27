# Multi-Factor Authentication

Cog supports multiple MFA methods that can be enabled from **Settings > Security**.

## TOTP (Authenticator App)

Time-based One-Time Passwords work with any authenticator app (Google Authenticator, Authy, 1Password, etc.).

### Setup

1. Go to **Settings > Security**
2. Click **Set up TOTP**
3. Scan the QR code with your authenticator app
4. Enter the 6-digit code to confirm

### Backup codes

After enabling TOTP, you receive a set of backup codes. Store these in a safe place -- each backup code can be used once to log in if you lose access to your authenticator app.

Regenerate backup codes from **Settings > Security** if needed.

## Email OTP

Receive a 6-digit code via email each time you log in. Requires [email to be configured](../admin/email.md) on the server.

### Setup

1. Ensure your account has an email address set in your profile
2. Go to **Settings > Security**
3. Click **Enable Email OTP**

Codes expire after 10 minutes.

## WebAuthn / Passkeys

Use hardware security keys (YubiKey) or platform authenticators (Touch ID, Windows Hello) for passwordless MFA.

### Setup

1. Go to **Settings > Security**
2. Click **Register Passkey**
3. Follow your browser's prompts to register the device
4. Name the passkey for identification

You can register multiple passkeys and remove individual ones.

## Login flow with MFA

1. Enter your username and password
2. If MFA is enabled, you are redirected to the MFA challenge page
3. Choose your method (TOTP, email OTP, or passkey) and verify
4. On success, you are logged in

App passwords bypass MFA -- they are intended for clients that cannot perform interactive MFA challenges.
