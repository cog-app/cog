# Email Configuration

Cog uses SMTP to send emails for password resets, email OTP codes, and notifications.

## Setup

Configure email from the admin panel at **Admin > Settings > Email**.

Required fields:

- **SMTP Host** -- mail server hostname (e.g. `smtp.example.com`)
- **SMTP Port** -- typically 587 (STARTTLS) or 465 (SSL)
- **Username** -- SMTP authentication username
- **Password** -- SMTP authentication password
- **From Address** -- sender address for outgoing emails (e.g. `cog@example.com`)

## Testing

After saving SMTP settings, use the **Send Test Email** button on the email settings page to verify the configuration works.

## Email templates

Cog ships with built-in email templates for:

- Password reset links
- Email OTP codes
- Group invitations

Admins can preview email templates at **Admin > Email > Preview**.

## Features that require email

- **Password Reset** -- users can request a password reset link via email from the login page
- **Email OTP** -- two-factor authentication via emailed one-time codes (see [MFA](../using-cog/mfa.md))

If SMTP is not configured, these features are unavailable. Users will see a message indicating email is not set up.
