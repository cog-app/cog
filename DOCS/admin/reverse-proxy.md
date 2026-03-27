# Reverse Proxy

Cog works behind any reverse proxy. Set `COG_BASE_URL` to match your public URL.

## Requirements

- Forward the `Host` header
- Forward `X-Forwarded-Proto` so Cog knows it's behind HTTPS
- Pass through WebDAV methods (PROPFIND, MKCOL, MOVE, COPY, PROPPATCH, REPORT, LOCK, UNLOCK)
- Do not buffer request bodies (needed for large file uploads)

## Caddy

```
cloud.example.com {
    reverse_proxy localhost:8580
}
```

Caddy automatically handles TLS and forwards the required headers.

## nginx

```nginx
server {
    listen 443 ssl;
    server_name cloud.example.com;

    client_max_body_size 0;  # No upload size limit

    location / {
        proxy_pass http://localhost:8580;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_request_buffering off;
    }
}
```

## Traefik

```yaml
http:
  routers:
    cog:
      rule: "Host(`cloud.example.com`)"
      service: cog
      tls:
        certResolver: letsencrypt
  services:
    cog:
      loadBalancer:
        servers:
          - url: "http://localhost:8580"
```

## Environment

```bash
export COG_BASE_URL="https://cloud.example.com"
```

The base URL is used for:

- Generating redirect URLs during login flows
- NextCloud Login Flow v2 grant URLs
- Public share links
- CalDAV/CardDAV service discovery
