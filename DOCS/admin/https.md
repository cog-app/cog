# Reaching Cog from Anywhere

Cog can serve HTTPS itself, with a free certificate from Let's Encrypt that
it renews on its own. This page is about the usual home setup: Cog on a
computer in your house, and your router passing connections from the
internet to it. Choose **Anywhere, over HTTPS** in setup and it does
everything here except the router.

If you already run Caddy, nginx or Traefik, choose **behind my reverse
proxy** instead and see [Reverse Proxy](reverse-proxy.md).

## What you need

1. **A domain name that points at your home.** Your home's public address
   is your router's internet (WAN) address. Most home addresses change now
   and then, so use a dynamic DNS service that follows it:
   [Duck DNS](https://www.duckdns.org) and [deSEC](https://desec.io) are
   free, and many routers can update them by themselves. If you own a
   domain, your DNS host may offer the same, or Cog's DNS check (below)
   works with it directly.
2. **Port forwarding on your router.** Setup's review page lists the rules
   with this computer's address filled in. For the default settings:

   | Name | Protocol | Public port | To this computer, port |
   |------|----------|-------------|------------------------|
   | Cog HTTPS | TCP | 443 | 8443 |
   | Cog SSH (optional) | TCP | 8522 | 8522 |

   Cog listens on 8443 rather than 443 so that it needs no administrator
   rights; the router translates. Look for "Port forwarding", "Virtual
   servers" or "NAT" in the router's settings.
3. **A fixed address for this computer on your network.** Routers hand out
   addresses that can change; make a "DHCP reservation" for this computer
   so the rules keep pointing at it.

Some internet providers (often mobile and satellite ones, and some fibre)
put many customers behind one address ("carrier-grade NAT"), and nothing
from outside reaches you. If your router's WAN address starts with `100.64`
to `100.127`, or does not match what <https://ifconfig.me> shows, ask your
provider for a public address, or use the reverse proxy route with a
tunnel such as Cloudflare Tunnel or Tailscale Funnel.

## How Let's Encrypt checks the name is yours

Setup offers three ways, which differ in what must be reachable:

- **Through the HTTPS port** (TLS-ALPN-01, the default). Let's Encrypt
  connects to port 443, which your router sends to Cog. Nothing else to
  forward.
- **Through port 80** (HTTP-01). For routers or providers that will not
  pass 443 until a certificate exists. Forward public port 80 to the
  computer's port 8080 too. Port 80 then also sends visitors on to HTTPS.
- **Through your DNS host** (DNS-01). Cog adds a record with your DNS
  host's API. No ports need forwarding for the certificate (people still
  need 443 to reach Cog), and it is the only way to get a wildcard
  certificate (`*.example.com`). Built in: Cloudflare, deSEC, DigitalOcean,
  Duck DNS, Gandi, Hetzner, Namecheap, OVH, Porkbun, RFC 2136 (BIND, Knot,
  PowerDNS), lego's `httpreq` and `exec`.

Setup asks for the certificate before it finishes, so a mistake shows up
there with what to check. If it fails, Cog still starts and tries again
every hour; once the problem is fixed it serves HTTPS without being set up
again. To experiment without running into Let's Encrypt's limits, tick
**Use Let's Encrypt's test service**, then run setup again without it.

## Keeping it safe

Anyone on the internet can reach your sign-in page once the port is open.

- **Use a long admin password and turn on two-factor sign-in** (Settings >
  Security). Admins can require it for everyone in Admin > General.
- Cog limits how fast anyone can guess passwords, per address and per
  account, over the web, WebDAV and SSH alike.
- **SSH**: setup turns account passwords off for SSH when Cog is reachable
  from the internet, so it takes keys and app passwords only, which cannot
  be guessed. Forwarding the SSH port is optional; leave it closed if you
  only use SFTP at home.
- **Keep Cog updated.** Leave the daily check on; Admin > Updates installs
  a release with one click, or setup can install them automatically.
- Plain HTTP stays on, but only for this computer (`127.0.0.1:8580`), for
  the command line and anything set up to use it before.

## Settings

Setup writes these to `cog.toml`; you can also set them by hand or with
environment variables (see [Configuration](../getting-started/configuration.md)).

```toml
base_url = "https://cloud.example.com"
addr = "127.0.0.1:8580"

[tls]
mode = "acme"                    # or "files", with cert_file and key_file
addr = ":8443"
domains = ["cloud.example.com"]
email = "you@example.com"        # optional
challenge = "tls-alpn-01"        # "http-01" (with http_addr), "dns-01"
# http_addr = ":8080"
# dns_provider = "duckdns"
# [tls.dns_env]
# DUCKDNS_TOKEN = "..."
# ca = "staging"
```

Certificates and the ACME account are kept in `data/acme`.

With `mode = "files"`, Cog serves the certificate in `cert_file` and
`key_file` and reloads them hourly, so a certificate another tool renews is
picked up without a restart.
