#!/usr/bin/env bash
# 把上游 main 合并进当前分支，并把同步结论写进 $GITHUB_OUTPUT 供 workflow 决策。
#
# 本脚本只负责「合并 + 检测」，不推送、不创建 issue——那些副作用交给 workflow，
# 这样本地也能直接跑一次看会不会冲突。
#
# 输出：
#   outcome       up-to-date | merged | conflicted
#   code_changed  true | false（仅 merged 时有意义）
#   files         冲突文件清单（仅 conflicted 时）
set -euo pipefail

upstream_url="${UPSTREAM_URL:-https://github.com/tbphp/gpt-load.git}"
branch="${BRANCH:-main}"
output="${GITHUB_OUTPUT:-/dev/stdout}"

git remote add upstream "$upstream_url" 2>/dev/null ||
  git remote set-url upstream "$upstream_url"

git fetch --no-tags upstream "$branch"
# 上游 tag 用于归属上游发布，也与 models.dev 目录同步无关，但保持本地可见便于对照。
git fetch --tags upstream

before="$(git rev-parse HEAD)"
upstream_head="$(git rev-parse "upstream/${branch}")"

if [ "$before" = "$upstream_head" ]; then
  echo "outcome=up-to-date" >> "$output"
  exit 0
fi

echo "本地 $before -> 上游 $upstream_head"

if ! git merge "upstream/${branch}" --no-edit; then
  {
    echo "outcome=conflicted"
    echo "files<<__SYNC_CONFLICT_EOF__"
    git diff --name-only --diff-filter=U
    echo "__SYNC_CONFLICT_EOF__"
  } >> "$output"
  # 让工作区回到合并前的干净状态，避免后续步骤在冲突态下操作。
  git merge --abort
  exit 0
fi

after="$(git rev-parse HEAD)"

# 合并成功但 HEAD 未变，说明本地已包含上游全部提交——fork 领先上游时的常态。
# 此时没有要推送的内容，更不该触发发布，否则每轮调度都会空跑一次发布。
if [ "$after" = "$before" ]; then
  echo "outcome=up-to-date" >> "$output"
  exit 0
fi

changed_files="$(git diff --name-only "$before" "$after")"

# 上游会改动 .github/workflows/，而 GITHUB_TOKEN 无权推送这类文件；只有带 workflow
# scope 的 PAT 能推。这里提前失败并给出明确原因，而不是让推送步骤抛一个含糊的错误。
if printf '%s\n' "$changed_files" | grep -q '^\.github/workflows/'; then
  if [ -z "${SYNC_UPSTREAM_TOKEN:-}" ]; then
    echo "::error::上游改动了 .github/workflows/，但仓库未配置 SYNC_UPSTREAM_TOKEN 密钥。GITHUB_TOKEN 无权推送 workflow 文件，请在仓库 Secrets 中配置一个带 workflow scope 的 PAT。"
    exit 1
  fi
fi

# 只有 Go 与 web 源码变化才值得重建镜像；文档、CI、脚本改动不触发发布。
if printf '%s\n' "$changed_files" | grep -qE '\.go$|^web/'; then
  code_changed=true
else
  code_changed=false
fi

{
  echo "outcome=merged"
  echo "code_changed=$code_changed"
} >> "$output"

echo "已合并上游，代码变更=$code_changed"
