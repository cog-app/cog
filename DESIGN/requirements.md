# Cog (Cloud On Go) Requirements

## Overview

Cog is a file storage and plugin platform designed to be a high-performance, Go-based alternative to NextCloud. It maintains API compatibility with NextCloud ecosystem apps while utilizing a modern, streaming-optimized architecture.

For canonical terminology and shared data shape outlines, see `DESIGN/concepts_and_types.md`.

## Core Features

- **File Access Protocols:**
  - **WebDAV:** Primary for NextCloud compatibility.
  - **SFTP:** High-performance transfers, compatible with `rsync`.
  - **Samba (SMB):** For native OS network drive mapping.
  - **FTP / FTPS:** Legacy compatibility.
  - **S3 API:** Allows Cog to act as an S3-compatible storage provider.
  - **Full Text Search:** Pluggable indexing (Bleve/SQLite FTS) per-user, enabling global search across files and plugin resources.
  - **Extensibility:** Architecture designed to easily plug in new protocol handlers.
- **Filesystem Abstraction:**
  - Internal interface inspired by `hackpadfs`.
  - Support for mounting volumes at arbitrary paths within a user's VFS.
  - Backends: Local Disk, S3, Google Drive, and others.
  - **Flat Storage:** Files are stored flat on disk.
  - **Zero-Scan Import:** Administrators can add files to user data directly without requiring a metadata scan.
- **Frontend:**
  - Primary stack: HTMX + `templ` templates.
  - Component library exported as a Go package for plugin use.

## Data Storage

- **User Data:** Stored in individual SQLite databases per user (server-side).
- **File Metadata:** Stored in the privileged `files` plugin database (`app_data/files/plugin.db`) per user.
- **Global Sharing State:** Stored in a system/global SQLite database for efficient cross-user and cross-group lookups.
- **Directory Structure:** A specific directory is allocated per user, plus shared storage areas.

## Plugin System

- **Trust Tiers:** Four levels — Protected (always-on, system-vital), Privileged (Go-only, internal APIs), Trusted (Go or gRPC, instance-wide), External (remote, sandboxed iframe, user-installable). See `concepts_and_types.md`.
- **Access Control:** Plugins can have isolated or full access to user SQLite data and files based on declared permissions.
- **Databases:** Each plugin gets a root database (plugin-wide) and per-user databases.
- **Capabilities:**
  - Register route handlers (APIs, Pages, Assets).
  - Define custom resources for sharing.
  - Register settings pages (auto-wrapped with sidebar navigation).
- **Security:** CSRF tokens scoped per-plugin. External plugins render in sandboxed iframes.
- **Standard Plugins:** Calendar, Contacts, and RSS (News) included as 1st party (trusted tier).

## Sharing & Groups

- **Groups:** Collections of users and other groups (arbitrarily nested). Groups do not have a VFS or mounts.
- **Resource Sharing:** Files, volumes, and plugin-defined objects can be shared with users or groups.
- **Volumes:** Shareable storage backends. Either system-level (admin-created) or user-owned. Users mount shared volumes anywhere in their own VFS.
- **Ownership:** Resources (like directories) can own other resources (files), implying access rights.
- **Management:** Plugins define resource keys/IDs; Cog manages the sharing logic.

## Developer Interface

- **Go Package:** A dedicated package provided to implement host/plugin interfaces and the gRPC bridge for external plugins.
- **Optimization:** API designed for streaming queries and file transfers.
