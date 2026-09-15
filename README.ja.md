# Master Checklist — AD スモークテスト → Confluence

*[English version here](README.md)*

`apps.applied.dev` でホストされている、自動運転のスモークテストの実行結果を記録する小さな
Webアプリです。テストエンジニアがチェックリストフォームに入力し、送信すると、
[Master Testing](https://appliedintuition.atlassian.net/wiki/spaces/NEURON/pages/2693234852/Master+Testing)
ページ配下（`NEURON` スペース内）にその月のサブページ(なければ自動作成)として
Confluenceページが作成されます。

デフォルトはダークモードです。ヘッダーからライトモードへの切り替えや、
英語/日本語の切り替えができます。

## チェックリストの項目

- テスト種別(マスターテスト/候補テスト)、タグ([Ext-Applied-Frontier/brain2](https://github.com/Ext-Applied-Frontier/brain2/tags)
  のGitHubタグから自動補完 — マスターは `scheduled-night` タグ、候補は `candidate` タグ)、
  日付、車両、テストエンジニア、コミットハッシュ(選択したタグが指すコミットを自動入力)、
  Slackスレッド、録画(Google Driveリンク)、実行ID、総合結果
- プリフライトチェック: `run_syscheck` の結果、`check_timesync` の結果、
  ソフトウェアのビルドと起動、ヘルスモニターが正常であること、
  `/media/hotswap1/frontier/` にログが記録されていること
- エンゲージメントチェック
- ディスエンゲージメントチェック: 実行ID、ステアリング左、
  ステアリング右、アクセル、ブレーキ、クルーズコントロール、e-stop、AD/MDボタン
- **クローズドループ**（任意）: 実行情報の「クローズドループテスト」チェックボックスを
  入れるとフォームにクローズドループ欄が追加されます — 実行ID、マニューバ、使用ルート、
  録画(Google Driveリンク)。内容はチェックボックスがオンだった場合のみConfluenceページの
  「クローズドループ」ブロックに出力され、オフの実行には何も追加されません。

## トラックから実行IDを取得

ページの最初の2つのセクション — **🔑 トラックSSHセットアップ（ローカルのみ）** と
**前回ビルドからの変更点** — は折りたたみ可能です。ヘッダー行をクリックして開閉できます。

各実行ID欄（実行情報・ディスエンゲージメント・クローズドループ）には
**🚚 トラックから取得** ボタンが表示されます。クリックすると、接続中のトラックにSSHし、
当日の最新の実行ログディレクトリを探して欄に入力します。実行情報のボタンは、
「ログ記録」チェックのメモ欄にも完全なログパスを書き込み、車両欄が空なら
ホスト名から車両番号を補完します。

- **ローカル実行時にのみ機能** — ボタンはどこでも表示されますが、デプロイされた
  アプリ(Cloud Run)にはトラックへの経路がないため、クリックすると
  「ローカルで実行中のみ動作する」旨のメッセージを即座に返します（SSHタイムアウトで
  待たされることはありません）。ローカルでは `TRUCK_SSH_ENABLED=true go run .`。
  有効化していれば `CONFLUENCE_DRY_RUN` と併用してもSSH取得は本物になります
  （Confluence側だけドライランにできます）。`TRUCK_SSH_ENABLED` なしのドライランでは
  ダミーの実行IDを返し、トラックなしでUIの流れを試せます。
- **自分のSSH資格情報を使用** — アプリはシステムの `ssh` を呼び出すだけなので、
  `~/.ssh` の鍵・エージェント・設定がそのまま使われます。鍵をアプリに保存しません。
- **コマンドは固定** — ブラウザから送れるのは車両番号（数字のみ、`VEHICLE_RANGE`
  内であることを検証）だけで、リモートスクリプトはサーバー側で固定生成します。
- **当日の実行を優先** — トラックの時計で当日の最新ランを採ります。当日まだ無い場合は
  全体の最新を使い、警告を表示します。接続したトラックと車両番号が違う場合も警告します。

### トラックごとのSSHセットアップ（共有IP問題の解決）

どのトラックも同じ `TRUCK_SSH_TARGET` で応答するため（このラップトップが接続している
もの）、単純な `ssh 192.168.1.11` は衝突します — 2台目以降はホスト鍵が違うため
OpenSSHが接続を拒否します。**🔑 トラックSSHセットアップ（ローカルのみ）** カードが
1台につき1回だけこれを解決します。鍵とエイリアスは**トラック番号の名前になるよう強制**
され、入力はトラック番号だけです（取得と同じく `VEHICLE_RANGE` で検証）：

1. `ssh-keygen -t ed25519 -f ~/.ssh/truck-805` — トラックごとに1つの鍵、パスフレーズなし。
2. `~/.ssh/config` に `Host truck-805` ブロック（HostName 192.168.1.11 / User applied /
   IdentityFile ~/.ssh/truck-805 / IdentitiesOnly yes /
   UserKnownHostsFile ~/.ssh/known_hosts.d/truck-805 / StrictHostKeyChecking accept-new）
   — トラックごとにホスト鍵の保存先が分かれるため、衝突しません。
3. 公開鍵をトラックにインストール — まず既存の鍵/エージェントで、失敗したら任意の
   パスワード欄に入力したパスワードで（`SSH_ASKPASS` 経由。パスワードがコマンドラインに
   現れることはなく、この1回のためだけに使われます）。以後 `ssh truck-805` と🚚ボタンは
   パスワードなしで動作します。

1〜2は常に実行され、べき等です（再実行は「すでに存在」と報告）。失敗しうるのは3だけで、
その場合は結果に手動で実行する `ssh-copy-id` の1行が表示されます。エイリアスが存在
すれば、その車両番号の取得は生のアドレスではなく `truck-<番号>` にSSHします。
ホスト環境(Cloud Run)ではこのエンドポイントはローカル専用の503を返し、ドライランでは
ローカル手順だけを実行し、鍵インストールをスキップしたと報告します。

**リモートログイン。** セットアップカードにはトラックのリモート（VPN）IPも入力できます
（任意。`100.65.197.86` のようなプレーンなIPv4アドレスとして検証されます）。入力すると、
2つ目のエイリアス `Host truck-805-remote`（`applied@<リモートIP>`宛て、同じトラック専用
鍵を共有し、専用のknown-hostsファイルを持ちます）が追加されるため、VPN上のどこからでも
`ssh truck-805-remote` が機能します。同じ1回のセットアップで両方の接続方法が使えます。

## ローカル開発

```sh
go run .
```

デフォルトでは Secret Manager から `confluence-token` シークレットを読み込もうとしますが、
ローカルでは動作しません。代わりにドライランで実行してください
(Confluence APIを呼ばず、送信されるはずのペイロードをログに出力します):

```sh
CONFLUENCE_DRY_RUN=true go run .
```

その後、http://localhost:8080 を開いてください。ドライランでは「トラックから取得」
ボタンがダミーの実行IDを返します。実際のトラックに対して試すには、
トラックのネットワークに接続した上で:

```sh
CONFLUENCE_DRY_RUN=true TRUCK_SSH_ENABLED=true go run .
```

## 設定(環境変数)

| 変数 | デフォルト | 用途 |
|---|---|---|
| `CONFLUENCE_BASE_URL` | `https://appliedintuition.atlassian.net/wiki` | Confluenceのベースurl |
| `CONFLUENCE_SPACE_KEY` | `NEURON` | ページを作成するスペース |
| `CONFLUENCE_PARENT_PAGE_ID` | `2693234852` | Master Testing ページのID |
| `CONFLUENCE_BOT_EMAIL` | — | Basic Auth に使うボットアカウントのメールアドレス |
| `CONFLUENCE_CANDIDATE_PARENT_PAGE_ID` | `2909896800` | 候補テストのフォルダID |
| `GITHUB_TAG_REPO_OWNER` | `Ext-Applied-Frontier` | タグ自動補完元のリポジトリのオーナー |
| `GITHUB_TAG_REPO_NAME` | `brain2` | タグ自動補完元のリポジトリ名 |
| `ADDR` | `:8080` | HTTPのリスンアドレス |
| `CONFLUENCE_DRY_RUN` | `false` | Secret Manager と Confluence/GitHub 呼び出しをスキップし、ログ出力のみ行う |
| `TRUCK_SSH_ENABLED` | `false` | `/api/truck/run_id` を実際のSSH取得にする（ボタンは常に表示。未設定のホスト環境ではローカル専用の説明を返す） |
| `TRUCK_SSH_TARGET` | `applied@192.168.1.11` | 接続中トラックへのSSH接続先 |
| `TRUCK_LOG_ROOT` | `/media/hotswap1/frontier` | トラック上のログルート |
| `TRUCK_SSH_BIN` | `ssh` | 呼び出すSSHバイナリ（テスト用のfake sshに差し替えるためのもの） |

## シークレットとデプロイ(apps-platform)

`project.toml` は `enable_secrets = true` に設定されています。Confluence APIトークンと、
GitHubのパーソナルアクセストークン(`Ext-Applied-Frontier/brain2` への読み取り権限が必要)を
一度アップロードしてください:

```sh
apps-platform app secret set confluence-token "<api-token>"
apps-platform app secret set github-token "<personal-access-token>"
apps-platform app deploy
```

アプリはこれらを `secrets.go` 内で、
`projects/$PROJECT_ID/secrets/master-checklist-<name>-token/versions/latest`
から Secret Manager クライアント経由で読み込みます。

## アクセス制御

このアプリは Applied の正社員(FTE)と `ext-frontier` 外部グループに限定するべきです。
このアクセス制限はアプリのコードではなく `apps-platform` のプロジェクト設定側で行います —
最初のデプロイ後に apps-platform のプロジェクト設定で構成してください。
