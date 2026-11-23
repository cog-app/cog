# Setup, Releases and Updates

Most people should install Cog by downloading one file and running it. This
covers what that file does, how releases are built and published, and how
an installed Cog updates itself.

## Who it is for

A person with a computer at home, a router, and maybe a domain name. Cog
runs as them (no administrator rights), starts when they sign in, listens
on unprivileged ports, and the router translates the internet's 443 to the
computer's 8443. A system service with its own account is the second
choice, for a machine that is a server. The wording of the wizard and the
docs assumes the first.

## Setup (`internal/setup`)

**Launch.** Bare `cog` runs setup instead of serving when Windows made a
console for the process (opened from Explorer: `GetConsoleProcessList`
returns only itself), or when nothing says where a server's data is: no
configuration file (`config.Find`), no `COG_CONFIG`/`COG_DB`/
`COG_DATA_DIR`/`COG_ADDR`, and no `system.db` in the working directory. With
a desktop it opens the browser wizard; without one it prints how to run
`cog setup` or `cog serve`. Existing deployments (Docker sets `COG_*`,
others have `system.db` beside them) keep serving. `cog setup` runs the
wizard explicitly, in the browser with a desktop and at the terminal
without (`--cli`, `--web`).

**Browser wizard.** An HTTP server on `127.0.0.1:<random>` with no accounts.
What stands in for signing in is a random token in the address setup opens
(`/start?token=`), which becomes a `SameSite=Strict` cookie and leaves the
address bar. Every request must name the loopback address in `Host`, so a
page cannot reach it through a DNS name it controls (rebinding), and every
form must come with `Origin` set to the wizard. Pages reuse the server's
stylesheet; which parts of the form show depends on CSS `:has()` so the
wizard needs no script beyond htmx polling the progress log.

**One question a page.** The browser wizard asks one question a page
(`Steps` in `steps.go`), skipping the pages a plan does not need: there is
no domain page unless Cog is to be reached from the internet. Each page
owns the plan fields it asks; `Plan.Problems` lists everything wrong with
a plan, and a page shows only its own, so an unanswered later question
does not block an earlier page. Once the review has been seen, a saved
page returns to it, by way of any page the change has left unanswered (a
new way in asks its own questions). A change to an install starts at the
review, a summary with a link to each question.

**Plan and apply.** The wizard (browser or terminal) makes a `Plan`: scope,
folder, admin, access (local, LAN over HTTP or self-signed HTTPS,
Tailscale, internet over HTTPS, reverse proxy),
ports, SSH, updates, firewall. `Apply` carries it out. A system plan needs
administrator rights; when setup has none, `Run` writes the plan to a
private temporary file and runs `cog setup apply` elevated (pkexec or sudo
on Linux, `osascript ... with administrator privileges` on macOS, UAC via
`ShellExecuteEx` on Windows), which removes the plan as soon as it has read
it (it holds the admin's password) and appends to a log the wizard tails.

`Apply`, for an install or a change:

1. stops the service if it runs;
2. makes the folder (`bin/`, `data/`) and, for a system service, the
   account (`cog`, `_cog` on macOS, the virtual `NT SERVICE\cog`);
3. copies the running binary to `bin/cog` unless it is that file;
4. writes `cog.toml`, starting from the existing one so settings the
   wizard does not ask about survive;
5. obtains the certificate, answering on the ports the server will use.
   This is done here, in the privileged process, because on Linux the
   service account may bind 80/443 only inside its unit (ambient
   `CAP_NET_BIND_SERVICE`). A failure is a warning: the server retries
   hourly;
6. gives the folder to the service account (`chown -R`, `icacls`);
7. runs `cog setup finish` *from the installed binary, as the service
   account*: it opens the database (migrating it) and makes or resets the
   admin. Run as that account so what it makes belongs to the server;
8. opens the firewall if asked (system scope only);
9. installs and starts the service, and waits for Cog to answer;
10. for Tailscale, runs `tailscale serve --bg --https=443
    http://127.0.0.1:<port>` (`funnel` for Funnel), and turns off what a
    previous setup set up (`cog.toml` records it as `install.tailscale`).
    A failure is a warning: Cog runs, at its local address.

Running setup again finds installs at the default folders and the one the
running binary belongs to (`Find`) and offers: change settings (the plan is
rebuilt from `cog.toml`, `FromConfig`), reset an admin's password (and
sign it out everywhere), or remove the service, keeping the data.

**Services (`internal/service`).** systemd units (system, or `--user` with
lingering), launchd jobs (`LaunchDaemon` with `UserName`, or a
`LaunchAgent`), a Windows service (virtual account, automatic delayed
start, restart on any failure) or, for one user on Windows, a scheduled
task at sign-in that runs Cog through `conhost --headless` so no console
window appears. Every manager restarts Cog when it exits: that is how an
update takes effect.

**`nosetup`.** Images and distribution packages build with
`-tags nosetup`: no setup or update commands, bare `cog` always serves,
and no updater (Admin > Updates says who updates the build). The Dockerfile
does; CI vets that build.

## Configuration file (`internal/config/file.go`)

TOML, read by `config.Load` over `config.Defaults()`, then `COG_*`
variables override it (`loadConfig`). Found by `--config`, `COG_CONFIG`,
or beside an installed binary (`<dir>/cog.toml` for `<dir>/bin/cog`), so
admin commands run from an install need nothing. Relative paths are taken
from the file's folder. Unknown keys are errors. Written `0600`: it can
hold DNS credentials.

## TLS (`internal/certs`)

`tls.mode = "acme"` obtains and renews a certificate with lego; `"files"`
serves (and hourly reloads) certificate files; `"self-signed"` makes an
ECDSA certificate for `tls.domains` (IP addresses become IP SANs), valid
397 days (browsers refuse longer even when trusted), kept in `data/tls`
and remade when it nears expiry or the names change. Setup makes it so it
can show the fingerprint.

**Tailscale** is not a TLS mode: Cog stays plain HTTP on loopback and
`tailscale serve` terminates HTTPS in front of it, trusted as a proxy
(`trusted_proxies = "127.0.0.1/32,::1/128"`). Setup drives the
`tailscale` command rather than embedding a node with `tsnet`, which would
add a second identity on the tailnet, its own sign-in, and much of
Tailscale to every binary. Challenges are answered by
the server's own listeners, not servers lego starts: tls-alpn-01 by the
HTTPS listener's `GetCertificate` (so forwarding 443 alone is enough),
http-01 by the optional plain listener (`tls.http_addr`, which otherwise
redirects to `base_url`). DNS-01 supports a curated list of providers
(`certs.DNSProviders`): every lego provider would add the AWS, Azure and
Google SDKs to every binary. Account and certificates live in
`data/acme`, accounts per CA so trying staging first leaves production
clean. Renewal 30 days before expiry, checked every 12 h, retried hourly.
`TestObtainWithTLSALPN` and `TestObtainWithHTTP01` issue real certificates
from Pebble in the test process.

With TLS on, the plain listener (`addr`) is bound to `127.0.0.1` by setup:
the command line and old local integrations keep working, and nothing
outside sees unencrypted HTTP.

## Releases

Development happens on GitLab; GitLab push-mirrors the repository,
including tags, to GitHub. A tag `v*` there runs
`.github/workflows/release.yml`:

- each platform builds on its own runner, natively, because Cog needs cgo
  (go-sqlite3, onnxruntime_go): linux amd64/arm64, windows amd64 (MinGW
  on `windows-2022`), darwin arm64, and darwin amd64 cross-compiled by
  clang on the arm64 runner. `-X .../internal/version.Version=<tag>`;
  each native binary must report its tag (`cog version`);
- `publish` writes `SHA256SUMS` and creates the GitHub release (a tag with
  a `-` suffix is a prerelease);
- `pages` rebuilds the site: the docs (`cog docs`, see below) and
  `releases.json`, which `go tool release manifest` builds from every
  GitHub release and its `SHA256SUMS`, signed with `COG_RELEASE_KEY`. The
  site is its own repository, `cog-app/cog-app.github.io`, served from its
  `main` branch at <https://cog-app.github.io>; the job pushes the result
  there with a deploy key. It also runs on every push to master.

**Docs.** The website is the documentation every Cog serves at `/docs/`
(readable signed out, linked from the account menu as Help). `DOCS/` is a
Go package embedding the Markdown; `internal/docs` renders it with one
`html/template` layout in Cog's stylesheet, for both. `cog docs --out
public` writes the static site: the pages, `search.json`, and the
`ui/static` files they load. Search is the same in both: every query word
must start a word of the page, the title counting most; Cog's navbar
search lists matching pages under Help (`Docs.Search`), and the docs'
own box runs `docs-search.js` over `search.json`, so the website searches
with no server. Each page is a `text/template` first: `.Site` is true on
the website, `.Version` is Cog's, and `{{ skip }}` leaves a page out
(installation, inside an installed Cog). A link to a page left out of Cog
goes to the website instead; a link to no page fails the build, and
`TestCogDocs` builds both.

Assets are named `cog-<goos>-<goarch>[.exe]`; the manifest keys them by
`<goos>-<goarch>`, which is what `version.Platform()` reports.

### Maintainer setup (once)

1. Add a GitLab push mirror to `cog-app/cog` (GitLab: Settings >
   Repository > Mirroring repositories), with a GitHub token that may push
   and edit workflows.
2. `cog-app/cog-app.github.io`: Settings > Pages > Deploy from a branch,
   `main`, `/`. Add a deploy key with write access (`ssh-keygen -t
   ed25519`) and put its private half in `cog-app/cog` as the secret
   `PAGES_DEPLOY_KEY`.
3. `cog-app/cog`: Settings > Secrets > Actions: `COG_RELEASE_KEY`, the private
   key from `go tool release keygen` (base64 Ed25519 seed). The public half
   is in `internal/update/keys.go`. `manifest` refuses a key that is not
   trusted there, so a mismatch fails the workflow rather than every
   client.
4. If the repository or Pages address differs from
   `https://cog-app.github.io/releases.json`, change
   `update.DefaultManifestURL` (or set it with `-ldflags -X`).

To release: `git tag v1.2.3 && git push origin v1.2.3` on GitLab.

## Updates (`internal/update`)

The trust root is the signature, not the transport: `releases.json.sig` is
an Ed25519 signature of `releases.json` by a key compiled into the binary
(`TrustedKeys`, a list so the key can be rotated across a release), and the
manifest carries each binary's SHA-256 and size. A compromised GitHub
account can withhold updates, not supply them, unless it also holds
`COG_RELEASE_KEY`.

`Checker` (wired by `cog_update.go`, absent with `nosetup`) checks a
minute after start and daily (`update.check`), and admins can check and
install from Admin > Updates; `update.auto` installs on sight. `Newer`
offers the highest semver above the running version with a binary for this
platform; prereleases only to prereleases; nothing to a build without a
release version.

`Install` downloads beside the binary (so the final step is a rename on
the same filesystem), verifies size and hash, runs the new binary's
`version` and requires it to say the release's version, then replaces the
running binary: a rename over it on Unix; on Windows, which will not
replace a running program's file, it moves the old one to `cog.exe.old`
(removed at the next start) first. Then it restarts: `runService` ends the
server with `errRestart`, `Main` closes everything and `update.Restart`
either exits with status 75 for the service manager to start it again
(systemd `Restart=always`, launchd `KeepAlive`, Windows recovery actions on
non-crash failures), or, when nothing supervises it, `exec`s itself (Unix)
or starts a new process and exits (Windows).

The service account owns the install folder, binary included, so the
server can update itself. The cost is that a compromised server can
rewrite its own binary; the alternative, a privileged updater beside an
unprivileged server, was judged not worth its moving parts for a home
server. `cog update` from an administrator's shell does the same and
restarts the service.

## Not covered

- Code signing. Windows SmartScreen and macOS Gatekeeper warn about an
  unsigned download the first time; the installation docs say how to get
  past them. Signing needs a certificate (Windows) and an Apple developer
  account (notarisation).
- Linux arm (32-bit) and Windows arm64 binaries.
- UPnP/NAT-PMP port mapping: the router rules are shown for the person to
  add, which works on every router and leaves nothing open they did not
  choose.
