---
title: devc スタンドアロン devcontainer 起動 CLI
version: 1.0
date_created: 2026-06-25
last_updated: 2026-06-25
owner: devc メンテナ
tags: [tool, cli, infrastructure, docker, devcontainer]
---

# はじめに

`devc` は Go で書かれた単一バイナリの CLI ツールであり、Docker Engine API を直接呼び出して [devcontainers](https://containers.dev/) の起動と管理を行う。
公式 `devcontainer` CLI の中核機能を、Node.js、npm、VS Code を必要とせずに再現する。
本仕様は、実装コードおよびテストコード（`*_test.go`）から導出した `devc` の観測可能な振る舞い、インターフェース、データ契約、制約を、再実装、拡張、検証が一意に行えるよう明文化したものである。

> 本仕様は実装（`cmd/devc`, `internal/*`）とテスト（各パッケージの `*_test.go`）の双方を参照して
> 作成している。各受け入れ基準（第5章）には、対応するテストの根拠を可能な範囲で併記する。

## 1. 目的とスコープ

### 目的
`devc` ツールの機能とインターフェース契約を完全に定義する。対象は以下を含む。
- devcontainer を作成、接続、停止、削除するホスト側コマンド群。
- コンテナ内で動作するプロセスに公開されるコンテナ内コマンド群。
- `devcontainer.json` およびユーザレベル設定のパース。
- OCI devcontainer features を用いたイメージビルド。
- 動的ポートフォワーディングとホストコマンド実行を可能にするホストデーモンプロトコル。
- コンテナメタデータ、ラベル付け、ライフサイクルオーケストレーション。

### スコープ
本仕様は `devc` バイナリと以下との相互作用を対象とする。
- ローカル Docker Engine（REST API 経由）。
- ローカルファイルシステム（ワークスペース、ユーザ設定、キャッシュ、ロックファイル）。
- devcontainer features を配信する OCI レジストリ。
- `docker compose` CLI（Compose モード）。

対象外: Docker Engine の内部実装、OCI レジストリサーバの振る舞い、サードパーティ feature の中身。

### 想定読者
`devc` を実装、保守、テスト、コード生成するソフトウェアエンジニア、およびその振る舞いを推論する必要のある自動エージェント。

### 前提
- 標準的な環境変数（`DOCKER_HOST` 等）経由で到達可能な Docker Engine が利用できる。
- ホストは Linux または macOS（`linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`）。
- 認証情報転送のため、ホストに `git`（任意で `gh`）が存在する。
- Compose モードでは `docker compose` CLI プラグインが導入済み。

## 2. 用語定義

| 用語 | 定義 |
|------|------|
| **devcontainer** | `devcontainer.json` で定義され、コンテナとして実行される開発環境。 |
| **ワークスペース** | `.devcontainer/devcontainer.json` を含むホスト側ディレクトリ。 |
| **ワークスペース ID** | ワークスペースの安定識別子: `{basename}-{sha256(絶対パス)[:8]}`。 |
| **feature** | devcontainer Features 仕様に準拠し `install.sh` を持つ、OCI 配信またはローカルのインストール単位。 |
| **ロックファイル** | `.devcontainer/devcontainer-lock.json`。再現ビルドのため feature の解決済みダイジェストを固定する。 |
| **デーモン** | `devc` が起動するホスト側 Unix ソケットサーバ。コンテナへバインドマウントされる。 |
| **イメージモード** | `image` または `build`（Dockerfile）定義からビルドする devcontainer。 |
| **Compose モード** | `dockerComposeFile` + `service` を用い `docker compose` で起動する devcontainer。 |
| **コンテナ内モード** | `devc` バイナリが管理対象コンテナ内で実行されている状態（`DEVC_CONTAINER=1` で判定）。 |
| **ライフサイクルフック** | `onCreateCommand`, `postCreateCommand`, `postStartCommand` のいずれか。 |
| **リモートユーザ** | コンテナ内で attach / exec する際の実効ユーザ。 |
| **OCI** | Open Container Initiative（レジストリおよびイメージ配信の標準）。 |
| **TTY** | 端末。インタラクティブに接続されたセッション。 |

## 3. 要件、制約、ガイドライン

### 機能要件

- **REQ-001**: ツールは環境変数 `DEVC_CONTAINER` を確認してホストモードとコンテナ内モードを
  区別しなければならない（SHALL）。値が `"1"` のときコンテナ内モードである。
- **REQ-002**: ホストモードでは `up`, `down`, `clean`, `rebuild`, `ls`, `exec` のサブコマンドを
  公開しなければならない。
- **REQ-003**: コンテナ内モードでは `info`, `env`, `dotfiles`, `port`, `host`, `rebuild` の
  サブコマンドを公開しなければならない。
- **REQ-004**: `up` は省略可能なワークスペースディレクトリ引数（既定: カレントディレクトリ）、
  繰り返し可能な `--publish`/`-p` フラグ、`--rebuild` フラグを受け付けなければならない。
- **REQ-005**: `rebuild` は `up --rebuild` と等価でなければならない。
- **REQ-006**: ツールはワークスペースを絶対パスに解決し、その ID を
  `{basename}-{sha256(絶対パス) の先頭8桁16進}` として計算しなければならない。同名 basename でも
  パスが異なれば ID は異なる。
- **REQ-007**: `.devcontainer/devcontainer.json` が存在しない場合、`up` は `name`（ワークスペースの
  basename）と既定ベースイメージ `mcr.microsoft.com/devcontainers/base:ubuntu` を含む最小テンプレートで
  ファイルを生成しなければならない。既存ファイルは変更してはならない（SHALL NOT）。
- **REQ-008**: ツールは次の `devcontainer.json` フィールドをパースしなければならない:
  `image`, `build`（`dockerfile`, `context`, `args`, `target`）, `features`, `remoteUser`,
  `workspaceFolder`, `containerEnv`, `onCreateCommand`, `postCreateCommand`, `postStartCommand`,
  `forwardPorts`, `appPort`, `dockerComposeFile`, `service`, `runServices`, `overrideCommand`。
  未知のフィールドは無視し、`Raw` マップに保持しなければならない。
- **REQ-009**: `dockerComposeFile` が存在する場合は Compose モード、なければイメージモードを
  使用しなければならない。
- **REQ-010**: `workspaceFolder` 未指定時、コンテナ内の既定ワークスペースフォルダは
  `/workspaces/{ワークスペース名}` でなければならない。
- **REQ-011**: ツールは `${XDG_CONFIG_HOME:-~/.config}/devc/config.json` からユーザレベル設定
  （`features`, `dotfiles`, `mounts`）を読み込まなければならない。ファイル不在時は空設定として
  扱い、エラーにしてはならない。
- **REQ-012**: ユーザレベル feature とプロジェクトレベル feature はマージしなければならない。
  キー衝突時はプロジェクトレベルを優先しなければならない。
- **REQ-013**: ポートは3つのソースから収集し、重複排除した上で次の優先順とすること:
  (1) `-p` CLI フラグ、(2) `forwardPorts`、(3) `appPort`。
- **REQ-014**: 裸のポート（`host:container` のコロン形式でない）は、要求ポートと同値の空きホスト
  ポートに解決しなければならない。占有時は最大100回まで1ずつインクリメントし、いずれも空きが
  なければ警告して裸ポートを使用しなければならない。
- **REQ-015**: イメージモードでは、ベースイメージ、feature 集合、ローカル feature の内容ダイジェスト
  から導出した決定的なイメージタグ `devc-{ワークスペースID}:{12桁16進}` を計算しなければならない。
- **REQ-016**: 計算したタグのイメージがローカルに既存で `--rebuild` 未指定なら、再ビルドせずに
  再利用しなければならない（キャッシュヒット）。
- **REQ-017**: ツールは OCI feature を、(レジストリ pull トークン取得 → マニフェスト取得 →
  先頭レイヤの blob ダウンロード → ローカルキャッシュ) の手順で解決し取得しなければならない。
  `./` 接頭辞付き参照はローカル feature として `{ワークスペース}/.devcontainer/{feature}` から
  読み込まなければならない。
- **REQ-018**: 各 feature は `install.sh` の実行によりインストールしなければならない。feature の
  オプション値は大文字化した環境変数として export し、`devcontainer-feature.json` の `containerEnv`
  はイメージ／コンテナ環境に適用しなければならない。オプションキーはアルファベット順で
  決定的に出力すること。install.sh が前提とするユーザ環境変数の注入は REQ-036 に従う。
- **REQ-019**: ツールはロックファイル `.devcontainer/devcontainer-lock.json` を読み書きし、feature
  参照ごとに解決済みの `version`（タグ）と `resolved`（sha256 ダイジェスト）を記録しなければならない。
  ロックファイルの JSON 出力はキーソートにより決定的でなければならない。
- **REQ-020**: 新規作成コンテナには絶対ワークスペースディレクトリを値とする
  `devcontainer.local_folder` ラベルを付与し、イメージモードでは `devc-{ワークスペースID}` という
  名前を付けなければならない。
- **REQ-021**: ワークスペースに対応する既存コンテナは、ラベル
  `devcontainer.local_folder={絶対ワークスペースディレクトリ}` でフィルタして特定しなければならない。
- **REQ-022**: イメージモードのコンテナコマンドは `sleep infinity` とし、ライフサイクル処理および
  対話作業は `docker exec` で行わなければならない。
- **REQ-023**: 新規コンテナ作成時、ライフサイクルフックは `onCreateCommand` → `postCreateCommand`
  → `postStartCommand` の順で実行しなければならない。既存コンテナの再起動時は `postStartCommand`
  のみ実行しなければならない（実行中と停止中いずれの既存コンテナでも postStart のみ）。
- **REQ-024**: ライフサイクルフックは文字列形式（`sh -c` で実行）、配列形式（exec 形式）、
  オブジェクト形式（名前付きマップ、キーのソート順で実行）に対応しなければならない。フック失敗は
  致命的とせず、警告して続行しなければならない。
- **REQ-025**: ツールはデーモンディレクトリのバインドマウント経由で `devc` バイナリと `meta.json`
  メタデータファイルをコンテナへ注入し、`devc` を `PATH` で解決可能にしなければならない
  （`/usr/local/bin/devc` → `/opt/devc/bin/devc` のシンボリックリンク）。
- **REQ-026**: ツールはホストの Git アイデンティティ（`user.name`, `user.email`）と、利用可能なら
  GitHub CLI トークン（`gh auth token`）をコンテナへ転送しなければならない。
- **REQ-027**: `exec` コマンドはリモートワークスペースフォルダにてリモートユーザとして実行中
  コンテナでコマンドを実行しなければならない。コマンド無指定なら `bash -l`、指定ありなら
  `bash -lc "<command>"` を実行する。ワークスペースは位置引数、`--dir`/`-d`、またはコマンドの
  前に置く `--` 区切りのいずれでも指定できなければならない。
- **REQ-028**: `down` はコンテナを停止（イメージモード）または `docker compose stop`（Compose
  モード）し、ボリュームを保持しなければならない。
- **REQ-029**: `clean` はコンテナとそのボリュームを削除しなければならない（イメージモード: 強制
  削除、Compose モード: `docker compose down -v --remove-orphans`）。
- **REQ-030**: `ls`（別名 `list`, `ps`）は `devcontainer.local_folder` ラベルを持つコンテナを
  作成時刻の降順で一覧表示し、ワークスペース、ステータス、ポート、稼働時間、パスを表示しなければならない。
- **REQ-031**: コンテナ内 `port` コマンドは、`<port>` または `<host>:<container>` 形式を受け付け、
  デーモンにポートフォワードを要求しなければならない。
- **REQ-032**: コンテナ内 `host` コマンドはデーモンにホスト上でのコマンド実行を要求し、その
  結合出力を返さなければならない。
- **REQ-033**: コンテナ内 `rebuild` コマンドは、コンテナセッション終了時に再ビルドを行うよう
  デーモンへ通知しなければならない。
- **REQ-034**: コンテナ内 `info` はコンテナメタデータを、`env` は `DEVC_*` 変数と `containerEnv` を
  表示し、`dotfiles sync` はステージングディレクトリからユーザホームへの dotfile シンボリック
  リンクを再生成しなければならない。
- **REQ-035**: Compose モードでは、オーバーライドファイル `.devcontainer/.devc-compose-override.yml`
  を生成して対象サービスに（`overrideCommand` が true のとき）`sleep infinity`、作業ディレクトリ、
  環境変数、マウント、ポートを注入し、feature はイメージビルド時ではなくコンテナ起動後の実行時に
  インストールしなければならない。
- **REQ-036**: feature の `install.sh` を実行する前に、devcontainer Features 仕様が定める実行時環境変数を
  設定しなければならない。`_REMOTE_USER` と `_CONTAINER_USER` には解決済みリモートユーザを、
  `_REMOTE_USER_HOME` と `_CONTAINER_USER_HOME` にはそのユーザのホームディレクトリを設定する。
  リモートユーザは、`remoteUser` 設定値、ベースイメージの `devcontainer.metadata` ラベルの `remoteUser`、
  ベースイメージの `USER`、`root` の順で解決しなければならない。ホームディレクトリは `/etc/passwd` から
  解決し、見つからなければ `root` は `/root`、それ以外は `/home/{ユーザ}` にフォールバックする。本要件は
  イメージモードと Compose モードの双方に適用する。

- **SEC-001**: デーモンソケットはコンテナ内 `/opt/devc/devc.sock` に作成し、ホストディレクトリ
  `/tmp/devc-daemon-{ワークスペース名}` を `/opt/devc` へバインドマウントすること。ソケットは
  それをマウントしたコンテナ内からのみ到達可能でなければならない。
- **SEC-002**: デーモンの `host` リクエスト型はホスト上で任意コマンドを実行する。これは意図的な
  機能であり、信頼レベルは Docker ソケットのマウントと同等である。ドキュメントは `devc` コンテナ内で
  信頼できないコードを実行するとホストアクセスを許すことを警告しなければならない。
- **SEC-003**: 転送されたホスト認証情報は `/tmp/devc-credentials` 配下にステージングしてコンテナへ
  マウントすること。イメージに焼き込んではならない。
- **SEC-004**: 自動ポートフォワーディングは、特権ポートや高位エフェメラルポートの意図しない転送を
  避けるため、1024〜32768（両端含む）のコンテナ待受ポートのみを対象としなければならない。

### 制約

- **CON-001**: ツールは、到達可能な Docker Engine（および Compose モード用の `docker compose`）以外に
  ランタイム依存を持たない単一の配布可能 Go バイナリでなければならない。
- **CON-002**: キャッシュに影響する生成物（イメージタグ、Dockerfile、ロックファイル、compose
  オーバーライド）はすべて、同一入力に対して決定的でなければならない（キーソート、安定順序）。
- **CON-003**: デーモンプロトコルは Unix ソケット接続上で、単一の JSON リクエストに続く単一の
  JSON レスポンスでなければならない。
- **CON-004**: Docker クライアントは API バージョンネゴシエーション付きで環境から初期化し、
  プロセスごとに一度だけ遅延生成しなければならない。
- **CON-005**: ローカル feature 参照は `./` 接頭辞で識別する。それ以外はすべて
  `{registry}/{org}/{repo}/{id}:{tag}` 形式の OCI レジストリ参照として扱う。タグ省略時は `latest`。

### ガイドライン

- **GUD-001**: フックや feature インストールの出力は、クリーンな UX のため要約（末尾数行のみ等）
  すべきであり、失敗時のみ詳細を展開すべきである（SHOULD）。
- **GUD-002**: 低速な処理（ビルド、セットアップ）は進捗表示（スピナー／ストリーム出力）を行うべき。
- **GUD-003**: 未知サブコマンドのエラーメッセージは正しい使い方を示唆すべき（例: `devc up <path>`）。

### パターン

- **PAT-001**: オーケストレーションロジックは、注入可能な依存集合（`Deps`）を介してイメージモードと
  Compose モードで共有し、「既存コンテナの処理」と「新規作成コンテナの確定処理」の2エントリポイントを
  持たなければならない。
- **PAT-002**: モード固有の振る舞い（イメージ vs Compose）は、共有オーケストレータへ異なる依存実装を
  与えることで表現し、フローを重複させてはならない。

## 4. インターフェースとデータ契約

### 4.1 ホストモードコマンド

| コマンド | 概要 | フラグ／引数 |
|----------|------|--------------|
| `up` | コンテナを起動して接続 | `[-p host:container]…`, `--rebuild`, `[workspace-dir]` |
| `rebuild` | `up --rebuild` と等価 | `[-p host:container]…`, `[workspace-dir]` |
| `down` | コンテナ停止（ボリューム保持） | `[workspace-dir]` |
| `clean` | コンテナとボリュームを削除 | `[workspace-dir]` |
| `ls`（`list`, `ps`） | devc コンテナ一覧 | なし |
| `exec` | コンテナ内でコマンド実行 | `-d/--dir <path>`, `[workspace-dir] [-- command…]` |

レガシー引数の正規化（`rewriteLegacyArgs`、`main_test.go` で検証）:
`nil/[]` → `["up"]`、`-h`/`--help` → `help`、`-V` → `--version`、`--clean` → `clean`、
`--rebuild` → `up --rebuild`。既知サブコマンド、パス、フラグはそのまま通過。

### 4.2 コンテナ内モードコマンド

| コマンド | 概要 | 引数 |
|----------|------|------|
| `info` | コンテナメタデータ表示 | なし |
| `env` | `DEVC_*` と `containerEnv` を表示 | なし |
| `dotfiles sync` | dotfile シンボリックリンク再生成 | なし |
| `port` | デーモン経由でポート転送 | `<port>` または `<host>:<container>` |
| `host` | ホスト上でコマンド実行 | `<command> [args…]` |
| `rebuild` | 終了時の再ビルドを要求 | なし |

### 4.3 デーモンプロトコル（Unix ソケット）

リクエスト（JSON）:
```json
{
  "type": "port | host | rebuild",
  "port": "8080",
  "command": ["cmd", "arg1"]
}
```
レスポンス（JSON）:
```json
{
  "OK": true,
  "Message": "人間可読のステータス",
  "Output": "host コマンドの stdout+stderr"
}
```

| `type` | 必須フィールド | 効果 |
|--------|----------------|------|
| `port` | `port` | ポートを解決し `localhost:host` → `container:container` を TCP プロキシ。 |
| `host` | `command` | ホスト上で実行し、結合出力を返す。 |
| `rebuild` | なし | rebuild 要求フラグを立て、セッション終了時に再ビルドを起動。 |

### 4.4 `devcontainer.json`（対応サブセット）

```jsonc
{
  "name": "string",
  "image": "string",
  "build": { "dockerfile": "string", "context": "string",
             "args": { "K": "V" }, "target": "string" },
  "features": { "ghcr.io/org/repo/id:tag": { "option": "value" } },
  "remoteUser": "string",
  "workspaceFolder": "string",
  "containerEnv": { "K": "V" },
  "forwardPorts": [3000],
  "appPort": 3000,                       // number / "3000:8080" / 混在配列
  "onCreateCommand":  "string | [..] | { name: cmd }",
  "postCreateCommand":"string | [..] | { name: cmd }",
  "postStartCommand": "string | [..] | { name: cmd }",
  "dockerComposeFile": "string | [string]",
  "service": "string",
  "runServices": ["string"],
  "overrideCommand": true                // 既定 true
}
```

### 4.5 ユーザ設定（`${XDG_CONFIG_HOME:-~/.config}/devc/config.json`）

```json
{
  "features": { "ghcr.io/org/repo/id:tag": { "version": "nightly" } },
  "dotfiles": ["~/.config/nvim", "~/.ssh"],
  "mounts": [ { "source": "~/work", "target": "/home/user/work" } ]
}
```

### 4.6 ロックファイル（`.devcontainer/devcontainer-lock.json`）

```json
{
  "features": {
    "ghcr.io/org/repo/id:tag": {
      "version": "1.2.3",
      "resolved": "sha256:abcdef…"
    }
  }
}
```

### 4.7 コンテナメタデータ（`/opt/devc/meta.json`）

```json
{
  "Version": "devc バージョン",
  "Project": "ワークスペース名",
  "WorkspaceDir": "ホスト側パス",
  "WorkspaceMount": "コンテナ側パス",
  "RemoteUser": "user",
  "Image": "タグ または compose サービス",
  "Ports": ["8080:3000"],
  "Features": ["ソート済み feature 参照"],
  "Dotfiles": ["パス"],
  "ContainerEnv": { "K": "V" },
  "CreatedAt": "RFC3339 UTC",
  "Mode": "image | compose",
  "Arch": "GOARCH"
}
```

### 4.8 既知のパス、ラベル、定数

| 名称 | 値 |
|------|-----|
| コンテナ内環境マーカー | `DEVC_CONTAINER=1` |
| デーモンディレクトリ（ホスト） | `/tmp/devc-daemon-{ワークスペース名}` |
| デーモンマウント先 | `/opt/devc` |
| デーモンソケット | `/opt/devc/devc.sock` |
| メタデータファイル（コンテナ） | `/opt/devc/meta.json` |
| コンテナ内バイナリ | `/opt/devc/bin/devc`（`/usr/local/bin/devc` にシンボリックリンク） |
| dotfiles ステージング | `/opt/devc-dotfiles` |
| 認証情報ステージング | `/tmp/devc-credentials` |
| ワークスペースラベル | `devcontainer.local_folder=<絶対ディレクトリ>` |
| イメージタグ形式 | `devc-{ワークスペースID}:{12桁16進}` |
| コンテナ名（イメージモード） | `devc-{ワークスペースID}` |
| Compose プロジェクト | `{ワークスペースID}_devcontainer` |
| Compose オーバーライドファイル | `.devcontainer/.devc-compose-override.yml` |
| feature キャッシュ | `~/.cache/devc/features/{digest}.tgz` |
| 自動転送ポート範囲 | 1024〜32768 |

### 4.9 出力フォーマット（`ui_test.go`、`ls_test.go` で検証）

非 TTY 時のステータスマーカー: `PrintDone`=`[ok]`、`PrintProgress`=`[..]`、`PrintWarn`=`[!!]`、
`PrintError`=`[ERR]`、`PrintDetail`=5スペースインデント。出力は ANSI エスケープ（色、カーソル、OSC、CR）を除去できなければならない。

`ls` の表示フォーマット:
- 稼働時間（`formatUptime`）: 非実行中は `-`。実行中は `30s` / `5m` / `1h30m` / `2h` / `1d1h` /
  `2d` のように時間粒度で自動選択。
- ポート（`formatPorts`）: 公開ポートなし（または `public=0`）は `-`、ホストとコンテナが同値なら
  `3000`、異なれば `8080→3000`、複数はカンマ区切り、重複は排除。
- 表示名: `devc-*` 名 → compose ラベル（`project/service`）→ コンテナ名 → コンテナ ID 先頭12文字。

## 5. 受け入れ基準

- **AC-001**: `.devcontainer/devcontainer.json` が無いワークスペースで `devc up` を実行すると、
  既定テンプレートからファイルが生成されコンテナが起動する。
  （`TestEnsureDevcontainerJSON_CreatesFile`）
- **AC-002**: ワークスペースと feature 集合が不変のとき、`devc up` を2回実行すると、計算した
  イメージタグが一致するため2回目はキャッシュ済みイメージを再利用する（再ビルドしない）。
  （`TestComputeImageTag` の決定性）
- **AC-003**: `--rebuild` を付けて `devc up --rebuild` を実行すると、既存コンテナとキャッシュ済み
  イメージが削除され、イメージが一から再ビルドされる。
- **AC-004**: `-p 3000` でホストポート 3000 が占有されているとき、ポート解決は最大100回まで次の
  空きポート（3001, 3002, …）を選択する。（`TestResolvePort`）
- **AC-005**: `forwardPorts`、`appPort`、`-p` がすべてポートを指定するとき、CLI ポートが優先され
  重複は排除される。（`TestCollectPorts`）
- **AC-006**: `dockerComposeFile` と `service` を持つ `devcontainer.json` で `devc up` を実行すると、
  `docker compose up -d --build` でサービスを起動し、オーバーライドファイルを生成し、feature を
  実行時にインストールする。（`TestParseComposeConfig_*`, `TestWriteComposeOverride_*`）
- **AC-007**: 新規作成コンテナでオーケストレーションを確定すると、`onCreateCommand` →
  `postCreateCommand` → `postStartCommand` がこの順（3フックグループ）で実行される。
  （`TestFinalizeNewContainer_LifecycleHookCount`）
- **AC-008**: 既存の停止中／実行中コンテナで `devc up` を実行すると、接続前に `postStartCommand`
  のみ（1フックグループ）が実行される。停止中は再起動後の新コンテナ ID でフックを実行する。
  （`TestHandleExistingContainer_Stopped*`, `TestHandleExistingContainer_Running`,
  `TestHandleExistingContainer_StoppedHookCount`）
- **AC-009**: 実行中コンテナで `devc exec <dir> -- go test ./...` を実行すると、ワークスペース
  フォルダにてリモートユーザとして `bash -lc "go test ./..."` が実行される。コマンド無指定なら
  `bash -l`。（`TestBuildExecCmd`）
- **AC-010**: コンテナ内シェルで `devc port 8080` を実行すると、デーモン経由でホスト
  `localhost:8080` からコンテナ `8080` への TCP プロキシが確立される。
- **AC-011**: コンテナ内シェルで `devc host <cmd>` を実行すると、`<cmd>` がホスト上で実行され、
  その結合出力がコンテナへ返る。（`TestDaemonHostCommand`）
- **AC-012**: feature 参照に対しイメージをビルドすると、ロックファイルにその解決済みタグと sha256
  ダイジェストが記録され、以降のビルドは固定ダイジェストを再利用する。出力は決定的（キーソート）。
  （`TestLockfileRoundTrip`, `TestLockfileRoundTrip_Deterministic`）
- **AC-013**: 作成された全コンテナに絶対ワークスペースパスを値とする `devcontainer.local_folder`
  ラベルを設定し、`ls`、`down`、`clean`、`exec` がそれにより特定できる。
- **AC-014**: コンテナが 1024〜32768 のポートで待ち受けているとき、自動ポート検出のポーリングで
  そのポートが自動転送され、既に静的バインド済みなら二重転送されない。
  （`TestParseProcNetTCP`〔LISTEN=`0A` のみ、16進ポート〕, `TestStaticPortSet`）
- **AC-015**: 同名 basename でも絶対パスが異なるワークスペースは異なる ID を持つ。
  （`TestResolveWorkspace_UniqueID`）
- **AC-016**: `devcontainer.metadata` ラベルから `remoteUser` を抽出する際、配列の最後の非空値が
  優先される。（`TestRemoteUserFromMetadata`）
- **AC-017**: `rebuild` 要求が `daemon.RebuildRequested()` フラグを立て、`Close()` でソケットファイルが
  削除される。（`TestDaemonRebuild`, `TestDaemonClose`）
- **AC-018**: `remoteUser` 未指定でベースイメージの `devcontainer.metadata` が `vscode` を示すとき、
  feature の `install.sh` は `_REMOTE_USER=vscode` と `_REMOTE_USER_HOME=/home/vscode` を受け取り、
  `su - "$_REMOTE_USER"` を行う feature（例: `claude-code`）が空ユーザで失敗しない。
  （`TestFeatureUserEnv`, `TestGenerateDockerfile_RemoteUserEnv`）

## 6. テスト自動化戦略

- **テストレベル**: 単体（設定パース、ポート解決、feature／ロックファイルロジック、デーモン
  パーサ、メタデータ、`Deps` 注入によるオーケストレーション、UI フォーマット）、結合
  （Docker 連携のビルド／up フロー）、E2E（実 Docker Engine に対する `up`/`exec`/`down`/`clean`）。
- **フレームワーク**: Go 標準 `testing` パッケージ。既存 `*_test.go`（`internal/config`,
  `internal/build`, `internal/daemon`, `internal/compose`, `internal/orchestrate`, `internal/ui`,
  `cmd/devc` 等）で用いられるテーブル駆動テストを踏襲する。
- **テストデータ管理**: 一時ワークスペースディレクトリと一時設定ファイルを使用。オーケストレーションは
  `Deps` のフェイク実装を注入し、単体テストで Docker を不要にする。
- **CI/CD 連携**: テストは `make test` で Docker 内実行、lint は `make lint`（golangci-lint）、
  ビルドは `make build` で再現可能。
- **カバレッジ要件**: 純ロジックのパッケージ（config, build タグ計算, daemon パース, orchestrate,
  ui）は高い単体カバレッジを維持すべき。Docker 依存経路は結合テストで担保する。
- **パフォーマンステスト**: 主要関心事ではない。イメージキャッシュ再利用（AC-002）が主要なレイテンシ
  対策であり、これを検証すべき。

## 7. 根拠と背景

- **Node.js 非依存と Docker API の直接呼び出し**（CON-001）: 公式 CLI に対する依存ゼロの単一バイナリ代替を
  提供することがプロジェクトの中心的動機。
- **決定的なタグと生成物**（CON-002, REQ-015, REQ-019）: 内容アドレス指定のイメージタグにより、
  安全かつ高速なキャッシュ再利用が可能になり、不要な再ビルドを防ぐ。ロックファイルとオプション出力の
  ソートも再現性のため。
- **依存注入による共有オーケストレーション**（PAT-001/PAT-002）: イメージモードと Compose モードで
  ライフサイクル、セットアップ、接続ロジックを共有し、重複を減らし、Docker なしで単体テスト可能にする。
- **ホストデーモン**（SEC-001/SEC-002）: 公式 CLI がエディタ経由で実現するコンテナ内の利便機能
  （動的ポート転送、ホストコマンド実行、終了時再ビルド）を、信頼モデルを明示したソケットで提供する。
- **Compose の実行時 feature インストール**（REQ-035）: Compose サービスは `devc` のイメージ
  パイプラインでビルドされないため、起動済みサービスコンテナへ実行時にインストールする。
- **ユーザレベル設定**（REQ-011/REQ-012）: グローバルな feature、dotfiles、mounts を全プロジェクトに
  適用しつつ、衝突時はプロジェクト設定を優先する。

## 8. 依存関係と外部統合

### 外部システム
- **EXT-001**：Docker Engine。REST API 経由でコンテナの build/create/start/stop/remove、exec、
  inspect、イメージ操作を行う。
- **EXT-002**：`docker compose` CLI。Compose モードのサービス管理（`up`, `stop`, `down`）を行う。

### サードパーティサービス
- **SVC-001**：OCI レジストリ（例: `ghcr.io`）。feature のトークン発行、マニフェスト取得、blob
  ダウンロードを担う。パブリック feature には匿名 pull が必要。

### インフラ依存
- **INF-001**：ローカルファイルシステム。ワークスペース、`~/.config/devc/config.json`、feature
  キャッシュ（`~/.cache/devc`）、ロックファイル、`/tmp` 配下のデーモンディレクトリを含む。
- **INF-002**：Unix ドメインソケット。ホストデーモンのトランスポートに用いる。

### データ依存
- **DAT-001**：`devcontainer.json`。主要なワークスペース設定ソース。
- **DAT-002**：ホストの Git 設定および GitHub CLI トークン。転送される認証情報。

### 技術プラットフォーム依存
- **PLT-001**：Go ツールチェイン。バイナリビルド用（ビルド時のみ。ランタイムは単一バイナリ）。
- **PLT-002**：対象 OS とアーキテクチャ。`linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`。

### コンプライアンス依存
- **COM-001**：義務付けはない。運用者は信頼できないコードを実行する際のホストアクセス信頼モデル
  （SEC-002）を認識しなければならない。

## 9. 例とエッジケース

```jsonc
// イメージモード（feature と転送ポート）
{
  "image": "mcr.microsoft.com/devcontainers/base:ubuntu",
  "features": { "ghcr.io/devcontainers/features/go:1": {} },
  "forwardPorts": [3000],
  "remoteUser": "vscode"
}
```

```jsonc
// Compose モード
{
  "dockerComposeFile": "docker-compose.yml",
  "service": "app",
  "runServices": ["app", "db"],
  "overrideCommand": true
}
```

```bash
# 裸ポートの自動解決エッジケース:
# 3000 を要求したが 3000 と 3001 が占有 → 3002 に解決
devc up -p 3000 ~/project

# `--` 後の明示コマンド付き exec
devc exec ~/project -- npm test       # 実行: bash -lc "npm test"
devc exec ~/project                    # 起動: bash -l（対話）
```

実装が扱わなければならないエッジケース:
- `devcontainer.json` 不在 → 既定テンプレート生成（AC-001）。
- ユーザ設定ファイル不在 → 空設定、非致命（REQ-011）。
- 候補ホストポート100個すべて占有 → 警告し裸ポートを使用（REQ-014）。
- ライフサイクルフック失敗 → 警告して続行（REQ-024）。
- デーモンディレクトリが書込不可 → 適切にクリーンアップ／無効化。
- 停止中コンテナの再起動 → `postStartCommand` のみ実行（AC-008）。
- `./` 接頭辞付き feature 参照 → 内容ハッシュをダイジェストとするローカル feature として扱う。
- feature アーカイブの gzip / 非 gzip tar の両対応、`install.sh` 欠落時はエラー
  （`TestExtractFeatureTar*`）。
- `/proc/net/tcp` と `/proc/net/tcp6` の同一ポート重複排除（`TestParseProcNetTCP_DuplicatePorts`）。

## 10. 妥当性確認基準

準拠する実装は以下を満たさなければならない。
1. 第5章のすべての受け入れ基準を満たす。
2. 同一入力に対し、決定的な生成物（イメージタグ、生成 Dockerfile、ロックファイル、compose
   オーバーライド）をバイト単位で同一に生成する（CON-002）。
3. コンテナの特定を `devcontainer.local_folder` ラベルのみで行う（REQ-021, AC-013）。
4. 第4.3章のデーモン JSON リクエスト／レスポンスプロトコルを厳密に実装する（CON-003）。
5. ポートの優先順と解決規則を遵守する（REQ-013, REQ-014）。
6. 新規と既存コンテナでのライフサイクルフック順序規則を適用する（REQ-023）。
7. feature の `install.sh` に `_REMOTE_USER`、`_CONTAINER_USER`、`_REMOTE_USER_HOME`、
   `_CONTAINER_USER_HOME` を注入する（REQ-036）。
8. 自動転送ポート範囲 1024〜32768 を強制し、静的バインド済みポートを二重転送しない
   （SEC-004, AC-014）。
9. `make test` と `make lint` を通過する。

## 11. 関連仕様と参考資料

- Development Containers Specification — https://containers.dev/
- Devcontainer Features 配信（OCI）— https://containers.dev/implementors/features-distribution/
- プロジェクト README — `../README.md`
- OCI Distribution Specification — https://github.com/opencontainers/distribution-spec
