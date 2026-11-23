# Storage

**Settings > Storage** shows how much space you use, and what else is in your files besides what you put there yourself: storage someone shared with you, and your own WebDAV servers.

## Usage

How much you keep in Cog, and your limit if your administrator set one. It counts your files, what apps keep for you, and your calendars and contacts. Files in a mount count towards the volume they are on instead.

## Mounts

A mount puts other storage at a folder of your choice in your files, such as `/Photos/Family`. Its files then show in the file browser, over WebDAV and over SFTP, like your own.

There are three kinds:

- **A volume**: a whole volume, or a folder of one (the **Subpath**), that you own or that someone shared with you.
- **Storage** (*Storage* badge): space of your own on a volume an administrator set aside for that.
- **Shared** (*Shared* badge): a folder someone shared with you.

**Access** says whether you can change the files or only read them. That is up to whoever shared it.

**Remove** takes a mount out of your files. The files themselves stay where they are.

## Adding a mount

Under **Add a mount**, pick the volume, type the folder to put it at (**Path**), and if you want only part of it, the folder inside it (**Subpath**).

Folders shared with you, and storage shared with you, are added from **Files > Shared with me** instead: open the share and click **Add to my files** or **Add storage**.

## Your own volumes

**My volumes** are storage you add yourself: a WebDAV server, such as another Cog, a NextCloud, or a NAS. Fill in **Add a WebDAV volume** with its address and how to sign in to it, then mount it under **Mounts**.

From the list you can **Browse** a volume, **Share** it with other people or groups (they can then mount it too), or **Delete** it.
