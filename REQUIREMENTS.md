# Cog (Cloud On Go) Requirements

## Overview
Cog is a file storage and plugin platform designed to be a high-performance, Go-based alternative to NextCloud. It maintains API compatibility with NextCloud ecosystem apps while utilizing a modern, streaming-optimized architecture.

## Core Features
- **NextCloud Compatibility:** API compatible with NextCloud apps for Authentication, Files, WebDAV, CalDAV, CardDAV, and News (RSS).
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
- **User Data:** Stored in individual SQLite databases per user (Server-side).
- **File Metadata:** Stored in the user's SQLite database.
- **Directory Structure:** A specific directory is allocated per user, plus shared storage areas.

## Plugin System
- **Architecture:** Plugins communicate via gRPC or direct Go interface (compiled-in).
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
- **Go Package:** A dedicated package provided to implement the Go interface for both the host and the plugin (gRPC bridge).
- **Optimization:** API designed for streaming queries and file transfers.
