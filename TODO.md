These tasks should be completed.

- [ ] Multifactor authentication
  - [ ] TOTP tokens
  - [ ] OAuth
  - [ ] Email password resets
  - [ ] Session invalidation on password change
- [ ] Admin
  - [ ] Move email to own settings section and allow configuring Go templates with the layout.
- [ ] File versioning (git-controlled directories?)
- [ ] Backup plugin with Restic REST API
- [ ] Plugin installation and permissions
  - [ ] Enforce DB_SANDBOX, DB_READ, FS_READ, ROUTE_GLOBAL_BIND, etc
  - [ ] Audit and minimize plugin API
  - [ ] Enable trusted protobuf plugins
  - [ ] Allow admins to install trusted plugins
  - [ ] Allow users to install external plugins for themselves
  - [ ] SQLite methods for read_file(), head_file(), file_exists(), file_size()
- [ ] Audit log activities to table and expose it via WebDAV
