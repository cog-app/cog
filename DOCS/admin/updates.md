# Updates

Cog installed with setup keeps itself up to date.

## How it works

Once a day Cog reads the list of releases published with this documentation
(`releases.json`). When there is a newer one, **Admin > Updates** shows it,
with a link to what changed, and **Install and restart**:

1. downloads the new program for this computer;
2. checks it against the checksum in the release list, and that it runs;
3. puts it in place of the running one;
4. restarts. The service brings Cog back on the new version within a few
   seconds; uploads and downloads in progress are interrupted.

With **Install new versions automatically** (setup, or `auto = true`
below), Cog does this as soon as it finds a release.

The release list is signed. Cog only believes a list signed with the key
its own build trusts, and only installs a program whose checksum the list
gives, so the download location being tampered with cannot install
anything.

## From the command line

```bash
cog update --check   # say whether there is a newer release
cog update           # install it and restart the service
```

## Settings

```toml
[update]
check = true    # look once a day (COG_UPDATE_CHECK)
auto = false    # install what it finds (COG_UPDATE_AUTO)
```

## When Cog cannot update itself

The updater writes the new program beside the old one, so the account Cog
runs as must be able to write to the `bin` folder. Setup arranges that.
If you moved the program somewhere it cannot write, Admin > Updates says
so; run `cog update` as an account that can, or download the release
yourself.

Container images and distribution packages are built without the updater:
update them the way you installed them (`docker pull`, your package
manager).

## Prereleases

A release whose version has a suffix (`v1.4.0-rc.1`) is a prerelease. It is
only offered to Cog that is itself running a prerelease, so to try one,
install it by hand; from then on you get the next prerelease or release,
whichever is newer.
