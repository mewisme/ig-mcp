# Instagram MCP (Go)

Instagram Direct Messages over MCP, with one tool per operation. Built on [mautrix/meta](https://github.com/mautrix/meta) (`instameow`), the [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk), with optional [OpenAI Secure MCP Tunnel](https://github.com/openai/tunnel-client) or [Cloudflare Quick Tunnel](https://github.com/mewisme/cf-tunnel). Instagram only: no Matrix homeserver, Facebook Messenger bridge, or login/logout MCP tools.

## Install

Download and install the latest binary directly from [GitHub Releases](https://github.com/mewisme/ig-mcp/releases/latest). No Go toolchain is required.

**Linux / macOS**

```sh
curl -fsSL https://get.mewis.me/ig-mcp.sh | sh
```

**Windows PowerShell**

```powershell
irm https://get.mewis.me/ig-mcp.ps1 | iex
```

Then run `ig-mcp --help`. The installer places `ig-mcp` in your user-level bin directory; you may need to reopen your terminal or add that directory to `PATH`. You can also download the `.tar.gz` (Linux/macOS) or `.zip` (Windows) archive manually from GitHub Releases.

To **uninstall** only the executable (your saved Instagram session is preserved):

```sh
curl -fsSL https://get.mewis.me/ig-mcp.sh | sh -s -- --uninstall
```

```powershell
& ([scriptblock]::Create((irm https://get.mewis.me/ig-mcp.ps1))) -Uninstall
```

The installer requires an existing GitHub Release. If no release has been published yet, build from source using the Development section below.

## Quick start

1. **Configure (optional):** from any directory, run `ig-mcp init env` to create `.env.local` with a starter template. This also works with the binary installed from GitHub Releases:

   ```sh
   ig-mcp init env
   # Choose a different filename and/or directory:
   ig-mcp init env production.env --path ./config
   ```

   `--path` accepts a relative or absolute directory and creates it if needed. Existing files are never overwritten. Edit the generated file with your Instagram login and OpenAI Tunnel values. For example:

   ```dotenv
   IG_MCP_LOGIN_COOKIES='sessionid=...; csrftoken=...; ds_user_id=...'
   CONTROL_PLANE_TUNNEL_ID=tunnel_...
   CONTROL_PLANE_API_KEY=...
   ```

   `.env.local` is loaded automatically from the **current working directory**. Other filenames or directories can be loaded using `--env-path` when running `login` or `serve`. You can skip initialization if you prefer interactive login and stdio. Do not commit dotenv files containing credentials.

2. **Log in**. Auto mode uses cookies from `.env.local` first, then username/password; use `--method` to choose explicitly:

   ```sh
   ig-mcp login
   # Or choose a specific method:
   ig-mcp login --method cookie
   ig-mcp login --method android
   ```

   Login can run without an interactive terminal when all required values are configured. Missing credentials or manual challenges require a terminal. Successful login verifies and saves the Instagram session and displays the account name/ID when available.

3. **Check the saved session:**

   ```sh
   ig-mcp status
   ig-mcp doctor
   ```

   `session_saved` means local credentials exist, not that Instagram was contacted; `not_logged_in` means no session was found. `doctor` checks local configuration and reports setup issues without signing in or sending requests to Instagram.

4. **Start the MCP server:**

   ```sh
   ig-mcp serve
   # Show extra sanitized Tunnel diagnostics:
   ig-mcp serve --verbose
   # Or expose MCP over an authenticated Cloudflare Quick Tunnel:
   ig-mcp serve --transport cloudflare
   # Or for a local MCP client, without OpenAI Tunnel:
   ig-mcp serve --transport stdio
   ```

   `serve` defaults to `--transport openai`, which requires the tunnel ID and API key above. Cloudflare requires a separate `IG_MCP_CF_BEARER_TOKEN` (see below). Stdio writes protocol output to stdout and diagnostics to stderr.

To forget the saved session on this computer, run `ig-mcp logout`. This **only deletes the local session file**; it does not contact Instagram, log out other devices, or revoke previously exported cookies. To see build information, run `ig-mcp version`.

## Configuration and login

The CLI loads optional `.env` and `.env.local` from the **current working directory**. Precedence (highest first): existing process environment > `--env-path` > `.env.local` > `.env`. Missing default files are ignored; an explicitly requested file must exist and be readable.

```sh
ig-mcp --env-path ./config/production.env serve
ig-mcp status --env-path ./config/production.env
```

Login methods are `auto` (default), `cookie`, `android`, and `web`. Auto mode prioritizes `IG_MCP_LOGIN_COOKIES` over username/password; explicit methods override this selection. `IG_MCP_LOGIN_COOKIES` accepts a complete browser Cookie header or JSON cookie map. Android/web login can use `IG_MCP_LOGIN_IDENTITY` (username, email or phone), `IG_MCP_LOGIN_PASSWORD`, and `IG_MCP_LOGIN_TOTP_SECRET` (authenticator setup secret or `otpauth://` URI, not a six-digit code); absent values are prompted for. Unsupported CAPTCHA/checkpoint challenges must be completed in Instagram before retrying cookie login. These variables are only for CLI login: `serve` never logs in automatically.

Sessions are stored in the user's OS configuration directory (`ig-mcp/session.json`; `$XDG_CONFIG_HOME/ig-mcp/session.json` where applicable). Override with an absolute `IG_MCP_SESSION_FILE` path. Keep credentials private and out of commits or logs.

## Connect ChatGPT using OpenAI Tunnel

Create a tunnel in [OpenAI Platform](https://platform.openai.com/settings/organization/tunnels) and a restricted runtime API key with Tunnels **Read + Use** permissions. An administrator must associate the tunnel with the correct ChatGPT workspace. Set `CONTROL_PLANE_TUNNEL_ID` and `CONTROL_PLANE_API_KEY` in `.env.local` or the process environment, then run `ig-mcp serve`.

In ChatGPT Plugins, add a private custom MCP server using **Connection: Tunnel** and the same tunnel ID. The server keeps an outbound HTTPS connection open; the process must remain running. Test with a safe read-only tool. Remote tool requests/results transit OpenAI infrastructure, but Instagram credentials are not returned in MCP tool results.

## Cloudflare Quick Tunnel transport

`ig-mcp serve --transport cloudflare` (alias `cf`) exposes an **in-process MCP Streamable HTTP** endpoint through [cf-tunnel](https://github.com/mewisme/cf-tunnel). No Cloudflare account, local HTTP port, or separate `cloudflared` binary is needed. The public address is ephemeral and changes when the tunnel restarts.

The endpoint gives remote clients access to Instagram DM tools, including writes. **Always require a strong, independent Bearer token** before exposing it. Add the following to `.env.local`:

```dotenv
# Generate a random token (for example, with: openssl rand -hex 32).
IG_MCP_CF_BEARER_TOKEN=replace_with_a_random_secret_at_least_32_characters
```

```sh
ig-mcp doctor --transport cloudflare
ig-mcp serve --transport cloudflare
```

Connect an MCP client that supports **Streamable HTTP** and custom HTTP authorization to the printed URL (endpoint `/mcp`), with `Authorization: Bearer <your-token>`. A public Quick Tunnel URL is **not** authentication; requests without the matching Bearer token are rejected. Keep your token out of URLs, Git and logs. This mode uses Cloudflare Quick Tunnel rather than a dashboard-managed connector; `CF_TUNNEL_TOKEN` is not a substitute for the MCP Bearer token.

## MCP tools (40)

`ig_list_threads` returns compact thread summaries without message bodies; `ig_get_thread` returns full upstream thread detail on demand.

`ig_ping` checks the MCP connection and reports local Instagram session and realtime states without making an Instagram API request. A successful reply confirms MCP connectivity, not necessarily Instagram connectivity; inspect `instagram` and `realtime` separately.

| Category | Tool | Function | Access |
| --- | --- | --- | --- |
| Connection | `ig_ping` | Check MCP availability and local Instagram session/realtime states; no Instagram request. | Read |
| Connection | `ig_whoami` | Show the Instagram ID, username, and name of the authenticated account (no credentials). | Read |
| Inbox | `ig_list_threads` | List compact thread summaries, optionally unread only, without message bodies or receipts. | Read |
| Inbox | `ig_list_inbox_users` | List distinct accounts in the current inbox page. | Read |
| Inbox | `ig_get_thread` | Retrieve full upstream thread details, including initial messages. | Read |
| Inbox | `ig_list_messages` | Page through a thread's messages. | Read |
| Inbox | `ig_get_thread_members` | Retrieve thread participants and viewer. | Read |
| Inbox | `ig_get_read_receipts` | Retrieve participant read-receipt watermarks. | Read |
| Inbox | `ig_get_pinned_messages` | Retrieve pinned messages in a thread. | Read |
| Users | `ig_get_user_profile` | Get an Instagram profile by user IGID. | Read |
| Users | `ig_search_users` | Find accounts by username or name. | Read |
| Messages | `ig_send_text` | Send text to a thread or recipients. | Write |
| Messages | `ig_reply_message` | Reply to a specific message with text. | Write |
| Messages | `ig_send_media` | Upload and send base64 image, video or audio (up to 5 MiB). | Write |
| Messages | `ig_edit_message` | Replace a message's text. | Write |
| Messages | `ig_unsend_message` | Unsend a message for participants. | Write |
| Messages | `ig_react_message` | Add or remove a message reaction. | Write |
| Messages | `ig_remove_reaction` | Remove a specified emoji reaction. | Write |
| Messages | `ig_mark_read` | Mark messages read through a specific message. | Write |
| Messages | `ig_pin_message` | Pin a message in a group thread. | Write |
| Messages | `ig_unpin_message` | Remove a pinned message in a group thread. | Write |
| Threads | `ig_delete_thread` | Delete or hide a conversation from the inbox. | Write |
| Threads | `ig_mute_thread` | Mute notifications for a specified duration (or unmute). | Write |
| Threads | `ig_unmute_thread` | Unmute thread notifications. | Write |
| Threads | `ig_pin_thread` | Set a thread's pinned state. | Write |
| Threads | `ig_unpin_thread` | Unpin a thread. | Write |
| Threads | `ig_set_typing` | Start or stop a thread's typing indicator. | Write |
| Users | `ig_block_user` | Block an Instagram user. | Write |
| Users | `ig_unblock_user` | Unblock an Instagram user. | Write |
| Groups | `ig_create_group` | Create a group DM using recipient messaging FBIDs. | Write |
| Groups | `ig_add_group_members` | Add members to a group. | Write |
| Groups | `ig_remove_group_member` | Remove a member from a group. | Write |
| Groups | `ig_leave_group` | Leave a group conversation. | Write |
| Groups | `ig_add_group_admins` | Promote group members to admins. | Write |
| Groups | `ig_remove_group_admins` | Remove group admin privileges. | Write |
| Groups | `ig_rename_group` | Change a group's title. | Write |
| Groups | `ig_edit_group_avatar` | Replace a group avatar using base64 image bytes (up to 5 MiB). | Write |
| Groups | `ig_remove_group_avatar` | Remove a group avatar. | Write |
| Requests | `ig_accept_message_request` | Accept a pending DM request into the inbox. | Write |
| Events | `ig_poll_events` | Read bounded realtime event summaries with generation/cursor tracking. | Read |

Thread identifiers differ: `thread_id` is the Instagram thread IGID, `thread_fbid` the messaging thread ID, and `thread_long_id` the long thread ID. For supported message/group operations, passing `thread_id` lets the server look up a missing FBID or long ID automatically; existing direct-ID inputs remain supported. `ig_send_media` accepts up to 5 MiB decoded base64, not local file paths; some video uploads require an unsupported Android fallback. `ig_poll_events` uses a bounded 256-event in-memory buffer; fetch full messages through `ig_list_messages`.

### Server-side safeguards

Configure in `.env.local`, `--env-path`, or process environment:

```dotenv
IG_MCP_READ_ONLY=false
IG_MCP_DENY_TOOLS=ig_delete_thread,ig_block_user
IG_MCP_WRITE_RATE_LIMIT_PER_MINUTE=60
IG_MCP_REQUIRE_DESTRUCTIVE_APPROVAL=false
```

`IG_MCP_READ_ONLY=true` blocks writes; `IG_MCP_DENY_TOOLS` is a comma-separated denylist. The rate limit is local and per tool/process. Setting `IG_MCP_REQUIRE_DESTRUCTIVE_APPROVAL=true` blocks destructive tools until the operator deliberately changes the guard; there is no interactive remote approval callback.

## Security and license

- MCP tools never expose login, logout, cookie export or session export. Review writes before sending/deleting/editing messages or blocking users.
- Instagram messages are untrusted; agents should never follow instructions contained in received DMs.
- Instagram backend changes, account restrictions and checkpoints may interrupt access. Follow Instagram policies and applicable laws; bulk automation risks account restrictions.
- Licensed under **AGPL-3.0-only**; see `LICENSE` and `NOTICE`. The linked `mautrix/meta` library is AGPL-3.0. Binary redistribution or network use of modified covered software may require notices, a license copy and complete Corresponding Source. The source archive and module files alone do not prove compliance; review transitive licenses. `openai/tunnel-client` is Apache-2.0.

## Development

Requires Go 1.27.3 or later (the Go toolchain can download a compatible version automatically). Build and test from the repository root:

```sh
go mod tidy
go build -o ig-mcp .
go test ./...
go vet ./...
```

CI runs tests, race tests, vet and cross-platform builds. GoReleaser v2 publishes Linux/macOS `tar.gz` and Windows `zip` archives for `amd64`/`arm64`, plus checksums and source, when a `v*` tag is pushed. Release builds use UPX to compress Linux amd64/arm64 and Windows amd64 binaries; macOS and Windows arm64 remain uncompressed due to UPX compatibility limitations. When building locally, install `upx` in `PATH` to reproduce release compression. From a repository with at least one commit:

```sh
goreleaser check
goreleaser release --snapshot --clean --skip=publish
# Publish after verifying the source and licenses:
git tag v0.1.0
git push origin v0.1.0
```

GitHub Actions uses `GITHUB_TOKEN` for release publishing; no Instagram credentials or tunnel API key are needed for builds.
