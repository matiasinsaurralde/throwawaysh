# throwawaysh

`throwawaysh` is a Go SSH service that starts one ephemeral `krun` microVM per SSH session and connects the session to a shell inside that VM.

## What It Does

- Accepts SSH connections (default `:2222`).
- Boots a session-scoped microVM for each session.
- Uses the VM console for non-PTY sessions.
- Uses a guest PTY agent for interactive terminal sessions (resize/signals/stdin forwarding).
- Keeps host-side service logs structured with `slog` (`text` or `json`).

## Requirements

- Go `1.25+`
- `libkrun` installed on the host
- A Linux rootfs directory for guest sessions (`--rootfs`)

## Build

Use the provided `Makefile`:

```bash
make build
```

This will:

- Build the service binary at `./throwawaysh`
- Build the guest agent for Linux (`arm64` by default)
- Install the guest agent into the configured rootfs at:
  `./rootfs/usr/local/bin/throwawaysh-guest-agent`

Useful targets:

```bash
make build-service
make build-agent
make install-agent
make test
make lint
make clean
```

On macOS, `make build-service` also codesigns the service binary using `cmd/throwawaysh/entitlements.plist`.

## Run

Minimal run:

```bash
go run cmd/throwawaysh/main.go --rootfs /path/to/rootfs
```

Or run the built binary:

```bash
./throwawaysh --rootfs /path/to/rootfs
```

Default credentials (when passwordless mode is disabled):

- Username: `test`
- Password: `test`

## Connect

```bash
ssh -p 2222 test@localhost
```

For passwordless mode:

```bash
./throwawaysh --rootfs /path/to/rootfs --allow-passwordless
ssh -p 2222 anyuser@localhost -o PreferredAuthentications=none -o PubkeyAuthentication=no
```

## PTY Guest Agent

Interactive SSH terminal sessions (`pty-req`) rely on the guest agent binary inside the rootfs:

- Expected guest path: `/usr/local/bin/throwawaysh-guest-agent`
- If missing, PTY session startup fails with a clear error.

Install helper script:

```bash
./install_guest_agent.sh ./rootfs
```

## Configuration

Flags:

- `--listen-addr` (default: `:2222`)
- `--host-key-path` (default: `server_key`)
- `--rootfs` (required)
- `--username` (default: `test`)
- `--password` (default: `test`)
- `--allow-passwordless` (default: `false`)
- `--log-level` (default: `info`; `debug|info|warn|error`)
- `--log-format` (default: `text`; `text|json`)
- `--version`

Environment variables (flag-compatible):

- `SSH_ADDR`
- `SSH_HOST_KEY_PATH`
- `SSH_ROOTFS`
- `SSH_USERNAME`
- `SSH_PASSWORD`
- `SSH_ALLOW_PASSWORDLESS`
- `SSH_LOG_LEVEL`
- `SSH_LOG_FORMAT`

## Example Commands

Custom listen/auth:

```bash
./throwawaysh --listen-addr :2222 --rootfs /path/to/rootfs --username demo --password demo
```

JSON logs:

```bash
./throwawaysh --rootfs /path/to/rootfs --log-level debug --log-format json
```

## Notes

- The server creates the SSH host key file at `--host-key-path` if it does not already exist.
- Each SSH session maps to an isolated VM lifecycle.
- Current service is intentionally simple and focused on per-session isolation over persistence.
