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
  日付、車両、テストエンジニア、コミットハッシュ、Slackスレッド、
  録画(Google Driveリンク)、実行ID、総合結果
- プリフライトチェック: `run_syscheck` の結果、`check_timesync` の結果、
  ソフトウェアのビルドと起動、ヘルスモニターが正常であること、
  `/media/hotswap1/frontier/` にログが記録されていること
- エンゲージメントチェック
- ディスエンゲージメントチェック: 実行ID、クローズドループ実行ID、ステアリング左、
  ステアリング右、アクセル、ブレーキ、クルーズコントロール、e-stop、AD/MDボタン

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
| `CONFLUENCE_CANDIDATE_PARENT_PAGE_ID` | `2909896800` | 候補テストのフォルダID |
| `GITHUB_TAG_REPO_OWNER` | `Ext-Applied-Frontier` | タグ自動補完元のリポジトリのオーナー |
| `GITHUB_TAG_REPO_NAME` | `brain2` | タグ自動補完元のリポジトリ名 |
| `ADDR` | `:8080` | HTTPのリスンアドレス |
| `CONFLUENCE_DRY_RUN` | `false` | Secret Manager と Confluence/GitHub 呼び出しをスキップし、ログ出力のみ行う |

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
