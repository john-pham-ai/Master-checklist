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

- タグ、日付、車両、テストエンジニア、コミットハッシュ、Slackスレッド、
  録画(Google Driveリンク)、実行ID、総合結果
- プリフライトチェック: `run_syscheck` の結果、`check_timesync` の結果、
  ソフトウェアのビルドと起動、ヘルスモニターが正常であること、
  `/media/hotswap1/frontier/` にログが記録されていること
- エンゲージメントチェック
- ディスエンゲージメントチェック: ステアリング左、ステアリング右、
  アクセル、ブレーキ、クルーズコントロール、e-stop、AD/MDボタン

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

その後、http://localhost:8080 を開いてください。

## 設定(環境変数)

| 変数 | デフォルト | 用途 |
|---|---|---|
| `CONFLUENCE_BASE_URL` | `https://appliedintuition.atlassian.net/wiki` | Confluenceのベースurl |
| `CONFLUENCE_SPACE_KEY` | `NEURON` | ページを作成するスペース |
| `CONFLUENCE_PARENT_PAGE_ID` | `2693234852` | Master Testing ページのID |
| `CONFLUENCE_BOT_EMAIL` | — | Basic Auth に使うボットアカウントのメールアドレス |
| `ADDR` | `:8080` | HTTPのリスンアドレス |
| `CONFLUENCE_DRY_RUN` | `false` | Secret Managerとconfluence呼び出しをスキップし、ログ出力のみ行う |

## シークレットとデプロイ(apps-platform)

`project.toml` は `enable_secrets = true` に設定されています。Confluence APIトークンを
一度アップロードしてください:

```sh
apps-platform app secret set confluence-token "<api-token>"
apps-platform app deploy
```

アプリはこのトークンを `secrets.go` 内で、
`projects/$PROJECT_ID/secrets/master-checklist-confluence-token/versions/latest`
から Secret Manager クライアント経由で読み込みます。

## アクセス制御

このアプリは Applied の正社員(FTE)と `ext-frontier` 外部グループに限定するべきです。
このアクセス制限はアプリのコードではなく `apps-platform` のプロジェクト設定側で行います —
最初のデプロイ後に apps-platform のプロジェクト設定で構成してください。
