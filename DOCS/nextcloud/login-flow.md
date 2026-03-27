# Login Flow v2

Login Flow v2 is the mechanism NextCloud clients use to authenticate without directly handling passwords. Cog implements this protocol for compatibility with NextCloud desktop and mobile clients.

## How it works

1. **Client initiates** -- the client sends `POST /index.php/login/v2` to the server
2. **Server responds** with a poll token and a grant URL
3. **User authenticates** -- the client opens the grant URL in a browser, the user logs in and clicks "Grant Access"
4. **Client polls** -- the client polls `POST /index.php/login/v2/poll` with the token
5. **Server responds** with an app password and the login name on success

The generated app password is a long-lived credential the client uses for all subsequent requests (WebDAV, OCS, etc.).

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | `/index.php/login/v2` | Initiate login flow |
| POST | `/index.php/login/v2/poll` | Poll for completion |
| GET | `/index.php/login/v2/grant` | Grant page (browser) |
| POST | `/index.php/login/v2/grant` | Confirm grant (browser) |

## Timeout

Login flow sessions expire after 10 minutes. If the user doesn't grant access within that time, the client must start a new flow.

## Troubleshooting

- **Client can't connect:** Ensure `COG_BASE_URL` is set correctly and accessible from the client
- **Grant page doesn't load:** Check reverse proxy configuration -- the grant URL must be reachable from the user's browser
- **Poll returns nothing:** The user hasn't granted access yet, or the session expired
