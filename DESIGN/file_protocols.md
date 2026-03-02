# File Access Protocols

Cog is designed as a multi-protocol storage gateway. All protocols interface with the same Virtual File System (VFS) and authentication layer, ensuring consistent permissions and metadata across all access methods.

## 1. Protocol Architecture

The Host provides a **Protocol Manager** that coordinates various listener services. Each service:
1.  **Authenticates** the user via Cog's central Auth Provider.
2.  **Mounts** the user's VFS (including their `files/` directory and any shared resources).
3.  **Translates** protocol-specific commands (e.g., SFTP `SSH_FXP_READ`, SMB `COM_READ`) into Cog VFS operations.

## 2. Supported Protocols

### WebDAV (HTTP)
- **Primary Use:** NextCloud mobile/desktop apps and generic web folders.
- **Implementation:** Integrated into the main web server. Supports locking and custom `OC-` headers.

### SFTP (SSH)
- **Primary Use:** High-speed transfers, developer tools, and `rsync`.
- **Implementation:** An embedded SSH server (using `golang.org/x/crypto/ssh`).
- **Auth:** Supports both passwords and SSH Public Keys (stored in the user's `user.db`).

### Samba / SMB
- **Primary Use:** Native Windows/macOS/Linux network mounts.
- **Implementation:** Leverages a Go-native SMB library (e.g., `github.com/hirochachacha/go-smb2` or similar).
- **Compatibility:** Focuses on SMB2/3 for security and performance.

### FTP / FTPS
- **Primary Use:** Legacy scanners, cameras, and older automation scripts.
- **Implementation:** Supports TLS (FTPS) for secure command and data channels.

### S3 API
- **Primary Use:** Using Cog as a backend for backups or cloud-native applications.
- **Implementation:** Implements a subset of the Amazon S3 REST API.
- **Auth:** Uses S3 Access Keys and Secret Keys generated within Cog.

## 3. Performance & Streaming

All protocol implementations must prioritize **Streaming I/O**.
- **Buffer Management:** Use `io.Copy` and `io.ReaderFrom/WriterTo` to minimize memory allocations.
- **Concurrency:** Protocol listeners are non-blocking and handle multiple simultaneous transfers per user session.

## 4. Permission Consistency

Since all protocols go through the VFS:
- **Sharing:** A file shared with "User B" via the Web UI will automatically be visible to User B when they connect via SFTP or Samba.
- **Sandboxing:** Plugins with `FILES_SANDBOX` permissions only see their own folders, regardless of the protocol used to access the host (if protocol-level plugin access is granted).
- **Audit Logs:** All file modifications are logged centrally, regardless of the protocol used.

## 5. Adding New Protocols

New protocols are implemented as **internal core components** of Cog. They are integrated directly into the binary to ensure maximum performance and security.

### Core Implementation
Protocol handlers must implement the internal `ProtocolHandler` interface:
```go
type ProtocolHandler interface {
    Name() string
    Start(vfs VFSProvider, auth AuthProvider) error
    Stop() error
}
```
This ensures that every new protocol (e.g., NFS or WebFS in the future) is built against the same VFS and authentication primitives as the existing ones.
