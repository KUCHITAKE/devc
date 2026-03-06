# devc

devcontainer をDocker Engine API で直接起動・管理するCLI。devcontainer CLI や Node.js は不要。

image ベースと Docker Compose ベースの両方に対応。ユーザー固有の features、dotfiles、マウントは `~/.config/devc/config.json` で設定する。

## インストール

```bash
# リリースバイナリから
gh release download --repo KUCHITAKE/devc -p 'devc_*_linux_amd64.tar.gz'
tar xzf devc_*.tar.gz && install -Dm755 devc ~/.local/bin/devc

# ソースから
git clone https://github.com/KUCHITAKE/devc.git && cd devc
make install
```

## 使い方

```bash
devc ~/project                           # コンテナ起動 & 入る
devc up -p 3000:3000 -p 5173 ~/project   # ポート指定付き
devc rebuild ~/project                   # リビルドして入る
devc down ~/project                      # 停止 (volumes 保持)
devc clean ~/project                     # コンテナ & volumes 削除
```

## サブコマンド

| コマンド | 説明 |
|----------|------|
| `up [flags] [dir]` | コンテナ起動 & 入る (デフォルト) |
| `down [dir]` | 停止 (volumes 保持) |
| `clean [dir]` | コンテナ & volumes 削除 |
| `rebuild [dir]` | リビルドして入る (`up --rebuild` のエイリアス) |

### `up` のフラグ

| フラグ | 説明 |
|--------|------|
| `-p, --publish` | ポート公開 (例: `-p 3000:3000`)。繰り返し指定可 |
| `--rebuild` | コンテナをゼロからリビルド |

## 対応する devcontainer.json

### image ベース

```jsonc
{
  "image": "mcr.microsoft.com/devcontainers/base:ubuntu",
  "features": { ... },
  "forwardPorts": [3000],
  "remoteUser": "vscode"
}
```

### build.dockerfile ベース

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

### Docker Compose ベース

```jsonc
{
  "dockerComposeFile": "docker-compose.yml",   // string | string[]
  "service": "app",                             // 必須: メインサービス名
  "runServices": ["app", "db"],                 // 省略可: 起動するサービス (省略時は全サービス)
  "overrideCommand": true                       // 省略可: デフォルト true → sleep infinity 注入
}
```

Compose モードでは `docker compose` CLI (v2) を使用。features はランタイムインストールされる。

## ユーザー設定

`~/.config/devc/config.json` でユーザー固有の設定を行う:

```json
{
  "features": {
    "ghcr.io/duduribeiro/devcontainer-features/neovim:1": { "version": "nightly" },
    "ghcr.io/anthropics/devcontainer-features/claude-code:1": {},
    "ghcr.io/jungaretti/features/ripgrep:1": {},
    "ghcr.io/devcontainers/features/github-cli:1": {}
  },
  "dotfiles": [
    "~/.config/nvim",
    "~/.claude",
    "~/.claude.json",
    "~/.ssh"
  ],
  "mounts": [
    { "source": "~/work", "target": "/home/user/work" }
  ]
}
```

- **features**: 全プロジェクト共通で注入する OCI features (プロジェクト側の features が優先)
- **dotfiles**: コンテナ内の `/opt/devc-dotfiles/` にマウントされ、シンボリックリンクで配置される
- **mounts**: 追加のバインドマウント

Git の `user.name` / `user.email` と `gh auth token` は自動的にコンテナに引き継がれる。

## ライフサイクルフック

devcontainer.json の以下のフックに対応:

- `onCreateCommand` — コンテナ初回作成時
- `postCreateCommand` — コンテナ作成後
- `postStartCommand` — コンテナ起動時 (再起動含む)

string、配列、オブジェクト形式いずれにも対応。

## ポート解決

ポートは以下のソースから収集される (CLI フラグが優先):

1. `-p` フラグ
2. `devcontainer.json` の `forwardPorts`
3. `devcontainer.json` の `appPort`

ベアポート (例: `3000`) はホスト側の空きポートを自動検出する。使用中の場合はインクリメントして空きを探す。

## 開発

Docker さえあれば開発できる。Go やリンターのインストールは不要。

```bash
make build            # バイナリビルド
make test             # テスト実行
make lint             # golangci-lint 実行
make clean-cache      # Go モジュール・ビルドキャッシュを削除
```

## リリース

```bash
git tag vX.Y.Z
git push origin vX.Y.Z
# GitHub Actions が goreleaser でクロスビルド & リリース作成
```

ビルド対象: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`
