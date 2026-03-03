# devc

devcontainer CLI のラッパー。Neovim (nightly)、Claude Code、ripgrep、GitHub CLI を自動注入して起動する。

既存の `devcontainer.json` はそのまま動く。機能は `--additional-features` で注入され、ポートは `forwardPorts` / `appPort` から自動変換される。

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

## 注入される機能

- [Neovim (nightly)](https://github.com/duduribeiro/devcontainer-features/tree/main/src/neovim)
- [Claude Code](https://github.com/anthropics/devcontainer-features/tree/main/src/claude-code)
- [ripgrep](https://github.com/jungaretti/features/tree/main/src/ripgrep)
- [GitHub CLI](https://github.com/devcontainers/features/tree/main/src/github-cli)

## ホストからのマウント

以下のディレクトリ/ファイルが存在する場合、自動的にコンテナへマウントされる:

| ホスト | 用途 |
|--------|------|
| `~/.config/nvim` | Neovim 設定 |
| `~/.claude` | Claude Code 設定 |
| `~/.claude.json` | MCP サーバー設定 |
| `~/.ssh` | SSH 鍵 |

Git の `user.name` / `user.email` と `gh auth token` もコンテナに引き継がれる。

## ポート解決

ポートは以下のソースから収集される (CLI フラグが優先):

1. `-p` フラグ
2. `devcontainer.json` の `forwardPorts`
3. `devcontainer.json` の `appPort`

ベアポート (例: `3000`) はホスト側の空きポートを自動検出する。使用中の場合はインクリメントして空きを探す。

## 開発

```bash
mise install          # Go, golangci-lint, pre-commit をインストール
make setup-hooks      # pre-commit フックを有効化
make lint             # golangci-lint 実行
make test             # テスト実行
make build            # バイナリビルド
```

## リリース

```bash
git tag v0.2.0
git push origin v0.2.0
# GitHub Actions が goreleaser でクロスビルド & リリース作成
```

ビルド対象: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`
