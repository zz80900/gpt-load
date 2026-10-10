---
name: release
description: "发布 gpt-load 私有镜像到 GHCR（ghcr.io/zz80900/gpt-load）：同步 origin/main、确定 v2.0.0-zz.N 版本号、触发 private-image.yml、同步推进 latest、补 git tag、核对 digest。只要用户说「发布」「发布版本」「发版」「发镜像」「release」「推版本」，或要求把当前代码变成可拉取的镜像/交付给部署环境，就使用本 skill——即使没有提到 GHCR、版本号或具体工作流。"
---

# Release：gpt-load 私有镜像发布

把当前 `main` 的代码发布为 GHCR 镜像 tag，并同步推进 `latest`。每一步都在实战中踩过坑，按顺序执行，别跳步。

## 核心事实（别重新推导）

| 事实 | 值 |
|---|---|
| 发布目标 | `github.com/zz80900/gpt-load`（私有仓库，remote 名 `origin`） |
| 上游 | `github.com/tbphp/gpt-load`（remote 名 `upstream`，**绝不 push**） |
| 镜像 | `ghcr.io/zz80900/gpt-load`，工作流 `.github/workflows/private-image.yml`（手动触发，交叉编译 linux/amd64+arm64） |
| 版本序列 | `v2.0.0-zz.N`，N 递增 |
| latest | **默认推进**（`push_latest=true`）。只有用户明确说「不推 latest」时才省略该参数 |
| git tag | `private-image.yml` 只有 `contents: read`，**不会自动打 tag**，发布后必须补 |

## 步骤

### 1. 同步远端，确定发布提交

```bash
git fetch origin
git status -sb                  # 看 ahead/behind
git merge origin/main           # 落后就先合并（merge，不 rebase）；冲突按仓库常态解决
```

- 并行会话可能推进过 `origin/main`（踩过：`--ref main` 构建的是远端 tip，不是你本地那个提交）。
- 合并后检查 `.github/workflows/release.yml` 是否被上游同步带回来——它属于上游发布渠道，存在就删掉。
- 工作区有未提交改动时先处置；不要替用户决定提交内容。

### 2. 确定版本号（只认 GHCR）

本地 git tag 会落后于 GHCR，**只查 GHCR**：

```bash
TOKEN=$(curl -s -m 20 "https://ghcr.io/token?scope=repository%3Azz80900%2Fgpt-load%3Apull&service=ghcr.io" | python -c "import json,sys;print(json.load(sys.stdin)['token'])")
curl -s -H "Authorization: Bearer $TOKEN" "https://ghcr.io/v2/zz80900/gpt-load/tags/list" | python -c "
import json,sys
d=json.load(sys.stdin)
zz=sorted(int(t.split('zz.')[1]) for t in d.get('tags',[]) if t.startswith('v2.0.0-zz.'))
print('zz 序号:', zz); print('max:', max(zz) if zz else None)
"
```

- 取 `max + 1`。**必须数值排序**：字典序里 `zz.10` 排在 `zz.2` 前面。
- 不要假设「上次 +1」——序号不保证单调（历史上推 zz.3 时 zz.4 已存在）。
- `v2.0.0-zz.17` 不可用（迁移锚定事故）；算出的号若已被占用，往上取未占用的。
- `gh` token 缺 `read:packages`，直接 `gh api` 查包会 403——用上面的匿名 pull token。

### 3. 推送并触发构建

```bash
git push origin main
gh workflow run private-image.yml --repo zz80900/gpt-load --ref main \
  -f version=v2.0.0-zz.N -f push_latest=true
```

- 触发前用 `git rev-parse origin/main` 与准备发布的提交比对——`--ref main` 取的是**远端** main。
- 触发成功后立刻记下 run URL/id。

### 4. 补 git tag

```bash
git tag v2.0.0-zz.N <发布提交>
git push origin v2.0.0-zz.N
```

### 5. 核对（硬证据，不看感觉）

```bash
gh run view <run-id> --repo zz80900/gpt-load --json status,conclusion,headSha
```

- `headSha` 必须等于发布提交，`conclusion` 必须为 `success`。
- 更硬：从构建日志核对 `org.opencontainers.image.revision` 等于发布提交；`DOCKER_METADATA_OUTPUT_TAGS` 会被按行宽截断，以 `DOCKER_METADATA_OUTPUT_JSON` 或 `--tag` 行为准。
- 确认 latest 挪动：用匿名 token 分别取 `https://ghcr.io/v2/zz80900/gpt-load/manifests/latest` 与 `/manifests/v2.0.0-zz.N`（`-H "Accept: application/vnd.oci.image.index.v1+json"`），比较 `Docker-Content-Digest` 响应头，两者应一致。

## 已知环境噪声

- 本机走代理（`127.0.0.1:7890`），`github.com` / `ghcr.io` 会**各自瞬时抖动**（SSL EOF、超时）。命令失败先重试 3-4 次（间隔几秒），别急着换方案。
- `sync-upstream.yml` 每 6 小时自动合并上游；无冲突且带来源码变更时会**自动打 `v2.0.0-zz.N` 并触发 private-image（写死 push_latest=true）**——所以发布前查 GHCR 经常会发现序号已自己前进。
- `fork-ci.yml` 是 push 侧唯一真正会跑的校验（web-ci + gofmt + go vet）；上游的 `ci.yml`/`release.yml` 在本仓库是死的。

## 完成后报告

版本号、发布提交 hash、run 链接、latest 是否推进、新 tag 与 latest 的 digest 是否一致。
