#!/usr/bin/env bash
# HiDeck 双轨离线备份脚本（通用 Linux / Docker 环境，可在飞牛 FNOS、Unraid 等宿主运行）
#
# 备份内容：
#   1) Fork 与上游的完整 Git 历史（Git Bundle，含全部 Tags）
#   2) 原版 HiDeck 2.1.23 Docker 镜像与 Fork 自建镜像（docker save 归档）
#   3) Docker Compose / 部署脚本 / 配置示例
#   4) 运行中的 HiDeck 持久化配置与 SQLite 数据（在线备份，不停止容器）
#   5) 镜像 Digest、架构、Commit、备份时间等元数据
#   6) SHA256 校验文件与恢复说明
#
# 用法：
#   HIDECK_BACKUP_DIR=/path/to/hideck-backup ./scripts/backup-to-nas.sh
#
# 关键环境变量：
#   HIDECK_BACKUP_DIR        必填。备份根目录（自动创建）
#   HIDECK_FORK_REPO         Fork 仓库（默认 https://github.com/zhangsan-nb/hideck）
#   HIDECK_UPSTREAM_REPO     上游仓库（默认 https://github.com/yibaiba/hideck；不可访问时仅告警）
#   HIDECK_UPSTREAM_IMAGE    原版镜像（默认 docker.io/yibaiba/hideck:2.1.23）
#   HIDECK_FORK_IMAGE        可选。Fork 自建镜像（如 ghcr.io/zhangsan-nb/hideck:fork-main），存在或可拉取时归档
#   HIDECK_RUNTIME_DIR       可选。运行中的 HiDeck 宿主目录（含 config/、data/）
#   HIDECK_CONTAINER_NAME    可选。容器名（默认 hideck），用于状态快照
#   HIDECK_GPG_RECIPIENT     可选。设置后对敏感文件（config/db）用 gpg 加密
#   HIDECK_RETENTION         可选。保留最近 N 份备份；默认 0 = 永不自动删除
#   HIDECK_MIN_FREE_KB       可选。最低剩余空间要求（默认 2097152 = 2GiB）
#
# 行为保证：
#   - 先在临时目录生成，校验通过后才原子移动到正式归档目录；绝不覆盖历史备份
#   - 不停止、不重启任何容器
#   - 失败即报错退出，临时目录自动清理

set -euo pipefail

log()  { printf '[%s] %s\n' "$(date '+%F %T')" "$*"; }
die()  { printf '[%s] ERROR: %s\n' "$(date '+%F %T')" "$*" >&2; exit 1; }

HIDECK_BACKUP_DIR="${HIDECK_BACKUP_DIR:-}"
HIDECK_FORK_REPO="${HIDECK_FORK_REPO:-https://github.com/zhangsan-nb/hideck}"
HIDECK_UPSTREAM_REPO="${HIDECK_UPSTREAM_REPO:-https://github.com/yibaiba/hideck}"
HIDECK_UPSTREAM_IMAGE="${HIDECK_UPSTREAM_IMAGE:-docker.io/yibaiba/hideck:2.1.23}"
HIDECK_FORK_IMAGE="${HIDECK_FORK_IMAGE:-}"
HIDECK_RUNTIME_DIR="${HIDECK_RUNTIME_DIR:-}"
HIDECK_CONTAINER_NAME="${HIDECK_CONTAINER_NAME:-hideck}"
HIDECK_GPG_RECIPIENT="${HIDECK_GPG_RECIPIENT:-}"
HIDECK_RETENTION="${HIDECK_RETENTION:-0}"
HIDECK_MIN_FREE_KB="${HIDECK_MIN_FREE_KB:-2097152}"

[ -n "$HIDECK_BACKUP_DIR" ] || die "HIDECK_BACKUP_DIR 未设置"

have() { command -v "$1" >/dev/null 2>&1; }
have docker || die "需要 docker"
have git || die "需要 git"
have sha256sum || die "需要 sha256sum"

mkdir -p "$HIDECK_BACKUP_DIR" "$HIDECK_BACKUP_DIR/logs"
[ -w "$HIDECK_BACKUP_DIR" ] || die "备份目录不可写: $HIDECK_BACKUP_DIR"

FREE_KB="$(df -Pk "$HIDECK_BACKUP_DIR" | awk 'NR==2 {print $4}')"
[ -n "$FREE_KB" ] || die "无法读取剩余空间"
if [ "$FREE_KB" -lt "$HIDECK_MIN_FREE_KB" ]; then
  die "磁盘剩余空间不足：需要至少 $((HIDECK_MIN_FREE_KB/1024))MiB，当前 $((FREE_KB/1024))MiB"
fi

TS="$(date +%Y%m%d-%H%M%S)"
RUN_NAME="backup-$TS"
LOG_FILE="$HIDECK_BACKUP_DIR/logs/backup-$TS.log"
exec > >(tee -a "$LOG_FILE") 2>&1
log "开始备份：$RUN_NAME"
log "备份根目录：$HIDECK_BACKUP_DIR"

TMP="$(mktemp -d "$HIDECK_BACKUP_DIR/.tmp-$RUN_NAME-XXXXXX")"
cleanup() { [ -d "$TMP" ] && rm -rf "$TMP"; }
trap cleanup EXIT

mkdir -p "$TMP"/{source,images,deploy,runtime,manifests,checksums}

# ---------------------------------------------------------------- source
log "== 1/6 Git Bundle =="
FORK_MIRROR="$TMP/source/hideck-fork-mirror.git"
UPSTREAM_MIRROR="$TMP/source/hideck-upstream-mirror.git"

GIT_TERMINAL_PROMPT=0 git clone --mirror --quiet "$HIDECK_FORK_REPO" "$FORK_MIRROR" \
  || die "Fork 仓库克隆失败：$HIDECK_FORK_REPO"
git -C "$FORK_MIRROR" bundle create "$TMP/source/hideck-fork.bundle" --all
git bundle verify "$TMP/source/hideck-fork.bundle" || die "Fork bundle 自校验失败"
FORK_HEAD="$(git -C "$FORK_MIRROR" rev-parse HEAD)"
FORK_TAGS="$(git -C "$FORK_MIRROR" tag | wc -l | tr -d ' ')"
log "Fork bundle OK（HEAD=$FORK_HEAD，tags=$FORK_TAGS）"

UPSTREAM_STATUS="skipped"
UPSTREAM_HEAD=""
if GIT_TERMINAL_PROMPT=0 git clone --mirror --quiet "$HIDECK_UPSTREAM_REPO" "$UPSTREAM_MIRROR" 2>/dev/null; then
  git -C "$UPSTREAM_MIRROR" bundle create "$TMP/source/hideck-upstream.bundle" --all
  git bundle verify "$TMP/source/hideck-upstream.bundle" >/dev/null
  UPSTREAM_HEAD="$(git -C "$UPSTREAM_MIRROR" rev-parse HEAD)"
  UPSTREAM_STATUS="ok"
  log "上游 bundle OK（HEAD=$UPSTREAM_HEAD）"
else
  log "WARN: 上游仓库不可访问，跳过上游 bundle（不影响本次备份）"
fi
rm -rf "$FORK_MIRROR" "$UPSTREAM_MIRROR"

# ---------------------------------------------------------------- deploy
log "== 2/6 部署文件 =="
DEPLOY_LIST=(
  docker-compose.yml docker-compose.runtime.yml docker-compose.source.yml
  docker-compose.build.yml docker-compose.caddy.yml docker-compose.caddy-dns.yml docker-compose.pcsc.yml
  deploy.sh deploy-binary.sh
  config/config.example.yaml
  docs/BACKUP_AND_RECOVERY.md
  scripts/backup-to-nas.sh scripts/restore-from-backup.sh
)
# 用一次浅克隆提取文件（mirror 目录已删除，重建轻量克隆）
DEPLOY_CLONE="$TMP/deploy/.repo"
GIT_TERMINAL_PROMPT=0 git clone --depth 1 --quiet "$HIDECK_FORK_REPO" "$DEPLOY_CLONE"
for f in "${DEPLOY_LIST[@]}"; do
  if [ -f "$DEPLOY_CLONE/$f" ]; then
    mkdir -p "$TMP/deploy/$(dirname "$f")"
    cp -a "$DEPLOY_CLONE/$f" "$TMP/deploy/$f"
  else
    log "WARN: 部署清单文件不存在，跳过：$f"
  fi
done
# fork 内示例 compose（若存在）
if [ -d "$DEPLOY_CLONE/examples" ]; then
  cp -a "$DEPLOY_CLONE/examples" "$TMP/deploy/examples"
fi
DEPLOY_COMMIT="$(git -C "$DEPLOY_CLONE" rev-parse HEAD)"
rm -rf "$DEPLOY_CLONE"

# ---------------------------------------------------------------- images
log "== 3/6 Docker 镜像归档 =="
save_image() {
  local ref="$1" out="$2"
  if ! docker image inspect "$ref" >/dev/null 2>&1; then
    log "本地无 $ref，尝试拉取..."
    docker pull "$ref" || return 1
  fi
  log "docker save $ref -> $out"
  docker save -o "$TMP/images/$out" "$ref" || return 1
  docker image inspect "$ref" > "$TMP/manifests/$(basename "$out" .tar).inspect.json"
  command -v python3 >/dev/null 2>&1 && python3 - "$TMP/manifests/$(basename "$out" .tar).inspect.json" <<'PY' || true
import json, sys
d = json.load(open(sys.argv[1]))[0]
print("  RepoDigests:", d.get("RepoDigests"))
print("  Created:", d.get("Created"), "| Arch:", d.get("Architecture"), "| Os:", d.get("Os"))
PY
  return 0
}

save_image "$HIDECK_UPSTREAM_IMAGE" "upstream-2.1.23.tar" \
  || die "原版镜像归档失败：$HIDECK_UPSTREAM_IMAGE（P0 资产，必须成功）"
if docker buildx version >/dev/null 2>&1; then
  docker buildx imagetools inspect "$HIDECK_UPSTREAM_IMAGE" \
    > "$TMP/manifests/upstream-2.1.23.imagetools.txt" 2>/dev/null || true
fi

FORK_IMAGE_STATUS="skipped"
FORK_IMAGE_DIGEST=""
if [ -n "$HIDECK_FORK_IMAGE" ]; then
  if save_image "$HIDECK_FORK_IMAGE" "fork-image.tar"; then
    FORK_IMAGE_STATUS="ok"
    log "Fork 镜像归档 OK：$HIDECK_FORK_IMAGE"
  else
    log "WARN: Fork 镜像归档失败（本地不存在且拉取失败）：$HIDECK_FORK_IMAGE"
    FORK_IMAGE_STATUS="failed"
  fi
fi

# ---------------------------------------------------------------- runtime
log "== 4/6 运行数据 =="
RUNTIME_STATUS="skipped"
if [ -n "$HIDECK_RUNTIME_DIR" ]; then
  [ -d "$HIDECK_RUNTIME_DIR" ] || die "HIDECK_RUNTIME_DIR 不存在：$HIDECK_RUNTIME_DIR"
  mkdir -p "$TMP/runtime/hideck"
  if [ -f "$HIDECK_RUNTIME_DIR/docker-compose.yml" ]; then
    cp -a "$HIDECK_RUNTIME_DIR/docker-compose.yml" "$TMP/runtime/hideck/"
  fi
  if [ -f "$HIDECK_RUNTIME_DIR/config/config.yaml" ]; then
    cp -a "$HIDECK_RUNTIME_DIR/config/config.yaml" "$TMP/runtime/hideck/config.yaml"
    chmod 600 "$TMP/runtime/hideck/config.yaml"
  fi
  if [ -f "$HIDECK_RUNTIME_DIR/data/hideck.db" ]; then
    if have python3; then
      python3 - "$HIDECK_RUNTIME_DIR/data/hideck.db" "$TMP/runtime/hideck/hideck.db" <<'PY'
import sqlite3, sys
src = sqlite3.connect('file:%s?mode=ro' % sys.argv[1], uri=True)
dst = sqlite3.connect(sys.argv[2])
src.backup(dst)
row = dst.execute('PRAGMA integrity_check').fetchone()
print('  在线备份 hideck.db integrity_check:', row[0])
if row[0] != 'ok':
    sys.exit(1)
dst.close(); src.close()
PY
    else
      log "WARN: 无 python3，使用文件拷贝（容器运行中可能不一致）"
      cp -a "$HIDECK_RUNTIME_DIR/data/hideck.db" "$TMP/runtime/hideck/hideck.db"
    fi
  fi
  docker ps -a --filter "name=^${HIDECK_CONTAINER_NAME}$" \
    > "$TMP/runtime/hideck/container-state.txt" 2>/dev/null || true
  docker inspect "$HIDECK_CONTAINER_NAME" > "$TMP/runtime/hideck/container-inspect.json" 2>/dev/null || true
  RUNTIME_STATUS="ok"
  log "运行数据归档 OK：$HIDECK_RUNTIME_DIR"
else
  log "未设置 HIDECK_RUNTIME_DIR，跳过运行数据（config/db 不在本次备份内）"
fi

# 可选加密敏感文件
if [ -n "$HIDECK_GPG_RECIPIENT" ]; then
  have gpg || die "设置了 HIDECK_GPG_RECIPIENT 但未安装 gpg"
  log "加密运行数据（gpg recipient=$HIDECK_GPG_RECIPIENT）"
  find "$TMP/runtime" -type f \( -name '*.yaml' -o -name '*.db' \) -print0 |
    while IFS= read -r -d '' f; do
      gpg --yes --batch --encrypt --recipient "$HIDECK_GPG_RECIPIENT" "$f" \
        || die "gpg 加密失败：$f"
      rm -f "$f"
    done
fi

# ---------------------------------------------------------------- manifest
log "== 5/6 元数据 =="
cat > "$TMP/manifests/backup-meta.json" <<EOF
{
  "backup_name": "$RUN_NAME",
  "created_at": "$(date -u +'%Y-%m-%dT%H:%M:%SZ')",
  "host": "$(hostname)",
  "fork_repo": "$HIDECK_FORK_REPO",
  "fork_head": "$FORK_HEAD",
  "fork_tag_count": $FORK_TAGS,
  "deploy_commit": "$DEPLOY_COMMIT",
  "upstream_repo": "$HIDECK_UPSTREAM_REPO",
  "upstream_bundle": "$UPSTREAM_STATUS",
  "upstream_head": "$UPSTREAM_HEAD",
  "upstream_image": "$HIDECK_UPSTREAM_IMAGE",
  "fork_image": "$HIDECK_FORK_IMAGE",
  "fork_image_status": "$FORK_IMAGE_STATUS",
  "runtime_dir": "$HIDECK_RUNTIME_DIR",
  "runtime_status": "$RUNTIME_STATUS",
  "gpg_encrypted": $( [ -n "$HIDECK_GPG_RECIPIENT" ] && echo true || echo false )
}
EOF
cat > "$TMP/README.txt" <<'TXT'
HiDeck 离线备份说明
=====================

目录结构：
  source/     Git Bundle（hideck-fork.bundle / hideck-upstream.bundle 如可用）
  images/     docker save 归档（upstream-2.1.23.tar 为原版镜像，P0 资产）
  deploy/     Docker Compose、部署脚本与配置示例
  runtime/    运行中的 HiDeck config.yaml 与 hideck.db（如备份时提供）
  manifests/  镜像 Digest、平台、Commit、备份时间等元数据
  checksums/  SHA256SUMS

恢复方式：
  1) 校验完整性：
       cd <本目录> && sha256sum -c checksums/SHA256SUMS
  2) 离线恢复镜像：
       docker load -i images/upstream-2.1.23.tar
       docker images --digests | grep hideck
  3) 恢复源码：
       git clone source/hideck-fork.bundle hideck-restored
       git -C hideck-restored remote set-url origin <你的仓库地址>
  4) 恢复部署与运行数据：使用 scripts/restore-from-backup.sh（默认 Dry Run）
  5) 全部操作可在无外网环境下完成。

注意：
  - upstream-2.1.23.tar 对应官方 2.1.23（多架构镜像的本地 amd64 归档）；
    多架构完整备份在 GHCR 标签 upstream-2.1.23，摘要见 manifests/。
  - runtime/ 含敏感信息（后台密码哈希、短信库等），请妥善保存/加密。
TXT

# ---------------------------------------------------------------- checksum
log "== 6/6 校验与归档 =="
( cd "$TMP" && find . -type f ! -path './checksums/*' -print0 | sort -z | xargs -0 sha256sum > checksums/SHA256SUMS )
( cd "$TMP" && sha256sum -c checksums/SHA256SUMS --quiet ) || die "SHA256 校验失败"
log "SHA256 校验通过"

FINAL="$HIDECK_BACKUP_DIR/$RUN_NAME"
[ -e "$FINAL" ] && die "目标目录已存在，拒绝覆盖：$FINAL"
mv "$TMP" "$FINAL"
trap - EXIT
ln -sfn "$RUN_NAME" "$HIDECK_BACKUP_DIR/latest"

MANIFEST_SHA="$(sha256sum "$FINAL/checksums/SHA256SUMS" | awk '{print $1}')"
log "备份完成：$FINAL"
log "SHA256SUMS 摘要：$MANIFEST_SHA"

if [ "$HIDECK_RETENTION" -gt 0 ] 2>/dev/null; then
  log "按保留数量 $HIDECK_RETENTION 清理旧备份"
  mapfile -t OLD < <(find "$HIDECK_BACKUP_DIR" -maxdepth 1 -type d -name 'backup-*' | sort)
  COUNT="${#OLD[@]}"
  if [ "$COUNT" -gt "$HIDECK_RETENTION" ]; then
    for ((i=0; i<COUNT-HIDECK_RETENTION; i++)); do
      log "删除旧备份：${OLD[$i]}"
      rm -rf "${OLD[$i]}"
    done
  fi
else
  log "未设置 HIDECK_RETENTION，保留全部历史备份"
fi

log "全部完成。"
