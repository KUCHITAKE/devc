# devc

A lightweight CLI that launches and manages [devcontainers](https://containers.dev/) using the Docker Engine API directly. No Node.js, no VS Code — just a single binary and Docker.

## Why devc?

The official devcontainer CLI requires Node.js and npm. devc is a standalone Go binary (~12MB) that talks to Docker directly.

- **Zero runtime dependencies** — Docker is all you need
- **User-level config** — Global features, dotfiles, and mounts shared across all projects
- **In-container CLI** — Forward ports, run host commands, and trigger rebuilds from inside the container
- **Full devcontainer.json support** — Image, Dockerfile, and Docker Compose modes with OCI features

## Installation

### From release

```bash
gh release download --repo KUCHITAKE/devc -p 'devc_*_linux_amd64.tar.gz'
tar xzf devc_*.tar.gz && install -Dm755 devc ~/.local/bin/devc
```

Binaries are available for `linux/amd64`, `linux/arm64`, `darwin/amd64`, and `darwin/arm64`.

### From source

```bash
git clone https://github.com/KUCHITAKE/devc.git && cd devc
make install  # builds in Docker, installs to ~/.local/bin/devc
```

## Usage

```bash
devc ~/project                           # start container & attach
devc up -p 3000:3000 -p 5173 ~/project   # with port forwarding
devc rebuild ~/project                   # rebuild from scratch
devc down ~/project                      # stop (volumes preserved)
devc clean ~/project                     # remove container & volumes
```

### Commands

| Command | Description |
|---------|-------------|
| `up [flags] [dir]` | Start container and attach (default) |
| `down [dir]` | Stop container, keep volumes |
| `clean [dir]` | Remove container and volumes |
| `rebuild [dir]` | Alias for `up --rebuild` |

### `up` flags

| Flag | Description |
|------|-------------|
| `-p, --publish` | Publish ports (e.g., `-p 3000:3000`). Repeatable |
| `--rebuild` | Force rebuild, discard cached image |

### Port resolution

Ports are collected from multiple sources (CLI flags take precedence):

1. `-p` flags
2. `forwardPorts` in devcontainer.json
3. `appPort` in devcontainer.json

Bare ports (e.g., `3000`) auto-detect an available host port, incrementing if the preferred port is in use.

## In-container commands

When inside a devc container, the `devc` binary is available with a different set of commands:

| Command | Description |
|---------|-------------|
| `devc info` | Show container metadata (project, image, ports, features) |
| `devc env` | Show injected environment variables |
| `devc port <port>` | Forward a port dynamically (e.g., `devc port 8080`) |
| `devc host <cmd>` | Execute a command on the host machine |
| `devc dotfiles sync` | Re-sync dotfile symlinks |
| `devc rebuild` | Request a rebuild on next exit |

## Configuration

### devcontainer.json

devc supports all three devcontainer modes:

**Image-based:**
```jsonc
{
  "image": "mcr.microsoft.com/devcontainers/base:ubuntu",
  "features": { "ghcr.io/devcontainers/features/go:1": {} },
  "forwardPorts": [3000],
  "remoteUser": "vscode"
}
```

**Custom Dockerfile:**
```jsonc
{
  "build": {
    "dockerfile": "Dockerfile",
    "context": "..",
    "args": { "VARIANT": "3.11" },
    "target": "dev"
  }
}
```

**Docker Compose:**
```jsonc
{
  "dockerComposeFile": "docker-compose.yml",
  "service": "app",
  "runServices": ["app", "db"],
  "overrideCommand": true
}
```

### Lifecycle hooks

The following devcontainer.json lifecycle hooks are supported (string, array, and object forms):

- `onCreateCommand` — runs on first container creation
- `postCreateCommand` — runs after creation
- `postStartCommand` — runs on every start (including restarts)

### User config (`~/.config/devc/config.json`)

User-level settings that apply to all projects:

```json
{
  "features": {
    "ghcr.io/duduribeiro/devcontainer-features/neovim:1": { "version": "nightly" },
    "ghcr.io/devcontainers/features/github-cli:1": {}
  },
  "dotfiles": [
    "~/.config/nvim",
    "~/.ssh"
  ],
  "mounts": [
    { "source": "~/work", "target": "/home/user/work" }
  ]
}
```

- **features** — OCI features injected into every container (project-level features take precedence on conflict)
- **dotfiles** — Paths mounted into the container and symlinked into the user's home directory
- **mounts** — Additional bind mounts

Git credentials (`user.name`, `user.email`) and GitHub CLI tokens (`gh auth token`) are automatically forwarded to the container.

## Security

devc runs a Unix socket daemon on the host (`/tmp/devc-daemon-{id}/devc.sock`) that is mounted into the container. This daemon enables:

- **Dynamic port forwarding** from inside the container
- **Host command execution** via `devc host <cmd>`
- **Rebuild requests** via `devc rebuild`

The socket is only accessible from within the container that mounts it. This is an intentional design — devc containers can execute arbitrary commands on the host through this socket. This is equivalent to mounting the Docker socket, which many devcontainer setups already do.

If you run untrusted code inside a devc container, be aware that it has host access through this mechanism.

## Development

Only Docker is required to build and test. No local Go installation needed.

```bash
make build         # build binary in Docker
make test          # run tests in Docker
make lint          # run golangci-lint in Docker
make install       # build & install to ~/.local/bin/devc
make clean-cache   # remove Go module/build cache volumes
```

## License

[MIT](LICENSE)
