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
  - Support for mounting directories within user directories.
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

- **Architecture:** Plugins can run externally over gRPC or internally via direct Go interfaces (compiled-in).
- **Privileged Internal Plugins:** Some built-in plugins (notably `files`) are in-process only and cannot be implemented over gRPC. These plugins may use host-only extension interfaces not available to third-party plugins.
- **Installation Levels:** User, Group, or System.
- **Access Control:** Plugins can have isolated or full access to user SQLite data and files.
- **Capabilities:**
  - Register route handlers (APIs, Pages, Assets).
  - Define custom resources for sharing.
- **Security:** CSRF tokens used to prevent data leakage between plugins.
- **Standard Plugins:** Calendar, Contacts, and RSS (News) included as 1st party.

## Sharing & Groups

- **Groups:** Collections of users and other groups (arbitrarily nested).
- **Resource Sharing:** Files and plugin-defined objects can be shared with users or groups.
- **Ownership:** Resources (like directories) can own other resources (files), implying access rights.
- **Management:** Plugins define resource keys/IDs; Cog manages the sharing logic.

## Developer Interface

- **Go Package:** A dedicated package provided to implement host/plugin interfaces and the gRPC bridge for external plugins.
- **Optimization:** API designed for streaming queries and file transfers.
