#!/usr/bin/env bash
# HiDeck 离线灾难恢复脚本（默认 Dry Run，全部操作可在无外网环境完成）
#
# 用法：
#   ./scripts/restore-from-backup.sh --backup-dir <备份目录> [选项]
#
# 选项：
#   --backup-dir DIR     必填。指向一次备份归档目录（含 checksums/SHA256SUMS）
#   --apply              正式执行（默认仅 Dry Run 打印计划）
#   --load-images        恢复镜像：校验后 docker load -i images/*.tar
#   --restore-git DIR    恢复源码：git clone <bundle> 到 DIR（目录必须不存在）
#   --restore-deploy DIR 恢复部署文件到 DIR（默认拒绝覆盖已有文件）
#   --restore-runtime DIR 恢复运行数据到 DIR（默认拒绝覆盖；DB 恢复提示先停容器）
#   --force              DIR 已存在或目标文件已存在时，先改名保留旧数据再恢复
#
# 安全设计：
#   - 第一步永远校验 SHA256SUMS，失败立即退出
#   - 默认 Dry Run；--apply 才写入
#   - 默认不覆盖任何已有文件；--force 也会先把旧文件改名为 *.pre-restore-<ts>
#   - 不停止、不重启任何容器；需要停容器时仅打印提示

set -euo pipefail

log() { printf '[%s] %s\n' "$(date '+%F %T')" "$*"; }
die() { printf '[%s] ERROR: %s\n' "$(date '+%F %T')" "$*" >&2; exit 1; }

BACKUP_DIR=""
APPLY=0
LOAD_IMAGES=0
RESTORE_GIT=""
RESTORE_DEPLOY=""
RESTORE_RUNTIME=""
FORCE=0

while [ $# -gt 0 ]; do
  case "$1" in
    --backup-dir) BACKUP_DIR="${2:?}"; shift 2 ;;
    --apply) APPLY=1; shift ;;
    --load-images) LOAD_IMAGES=1; shift ;;
    --restore-git) RESTORE_GIT="${2:?}"; shift 2 ;;
    --restore-deploy) RESTORE_DEPLOY="${2:?}"; shift 2 ;;
    --restore-runtime) RESTORE_RUNTIME="${2:?}"; shift 2 ;;
    --force) FORCE=1; shift ;;
    -h|--help)
      sed -n '2,20p' "$0"; exit 0 ;;
    *) die "未知参数：$1" ;;
  esac
done

[ -n "$BACKUP_DIR" ] || die "缺少 --backup-dir"
[ -d "$BACKUP_DIR" ] || die "备份目录不存在：$BACKUP_DIR"
TS="$(date +%Y%m%d-%H%M%S)"

log "备份目录：$BACKUP_DIR"
if [ "$APPLY" = 1 ]; then log "模式：apply（正式执行）"; else log "模式：dry-run（默认，不写入任何文件）"; fi

# ------------------------------------------------------------------ 校验
SUMS="$BACKUP_DIR/checksums/SHA256SUMS"
[ -f "$SUMS" ] || die "缺少校验文件：$SUMS"
log "校验 SHA256SUMS ..."
( cd "$BACKUP_DIR" && sha256sum -c checksums/SHA256SUMS --quiet ) || die "SHA256 校验失败，拒绝恢复"
log "SHA256 校验通过"

if [ -f "$BACKUP_DIR/manifests/backup-meta.json" ]; then
  log "备份元数据："
  cat "$BACKUP_DIR/manifests/backup-meta.json"
fi

plan() { log "计划：$*"; }
run()  {
  if [ "$APPLY" = 1 ]; then
    log "执行：$*"
    "$@"
  else
    plan "$*"
  fi
}

# ------------------------------------------------------------------ 镜像
if [ "$LOAD_IMAGES" = 1 ]; then
  for tar in "$BACKUP_DIR"/images/*.tar; do
    [ -f "$tar" ] || continue
    plan "docker load -i $tar"
    if [ "$APPLY" = 1 ]; then
      docker load -i "$tar" || die "镜像导入失败：$tar"
    fi
  done
  if [ "$APPLY" = 1 ]; then
    docker images --digests | grep -i hideck || true
  fi
fi

# ------------------------------------------------------------------ Git
if [ -n "$RESTORE_GIT" ]; then
  for bundle in "$BACKUP_DIR"/source/hideck-fork.bundle "$BACKUP_DIR"/source/hideck-upstream.bundle; do
    [ -f "$bundle" ] || continue
    plan "git bundle verify $bundle"
    if [ "$APPLY" = 1 ]; then
      git bundle verify "$bundle" >/dev/null || die "bundle 校验失败：$bundle"
    fi
    base="$(basename "$bundle" .bundle)"
    target="$RESTORE_GIT"
    if [ -e "$target" ]; then
      if [ "$FORCE" = 1 ]; then
        log "目标已存在，改名保留：$target -> $target.pre-restore-$TS"
        run mv "$target" "$target.pre-restore-$TS"
      else
        die "目标目录已存在（--force 可先改名保留）：$target"
      fi
    fi
    plan "git clone $bundle $target"
    if [ "$APPLY" = 1 ]; then
      git clone "$bundle" "$target" || die "Git 恢复失败"
      log "恢复完成：$target（HEAD=$(git -C "$target" rev-parse HEAD)）"
    fi
    break
  done
fi

# ------------------------------------------------------------------ 部署文件
if [ -n "$RESTORE_DEPLOY" ]; then
  [ -d "$BACKUP_DIR/deploy" ] || die "备份中没有 deploy/ 目录"
  for src in "$BACKUP_DIR"/deploy/*; do
    [ -e "$src" ] || continue
    dst="$RESTORE_DEPLOY/$(basename "$src")"
    if [ -e "$dst" ]; then
      if [ "$FORCE" = 1 ]; then
        log "已存在，改名保留：$dst -> $dst.pre-restore-$TS"
        run mv "$dst" "$dst.pre-restore-$TS"
      else
        die "目标已存在（--force 可先改名保留）：$dst"
      fi
    fi
    plan "cp -a $src -> $dst"
    if [ "$APPLY" = 1 ]; then
      mkdir -p "$RESTORE_DEPLOY"
      cp -a "$src" "$dst"
    fi
  done
fi

# ------------------------------------------------------------------ 运行数据
if [ -n "$RESTORE_RUNTIME" ]; then
  [ -d "$BACKUP_DIR/runtime" ] || die "备份中没有 runtime/ 目录"
  log "提示：恢复 hideck.db 前应先停止 HiDeck 容器；本脚本不会自动停止容器。"
  find "$BACKUP_DIR/runtime" -type f -print0 | while IFS= read -r -d '' src; do
    rel="${src#"$BACKUP_DIR/runtime"/}"
    dst="$RESTORE_RUNTIME/$rel"
    if [ -e "$dst" ]; then
      if [ "$FORCE" = 1 ]; then
        log "已存在，改名保留：$dst -> $dst.pre-restore-$TS"
        run mv "$dst" "$dst.pre-restore-$TS"
      else
        die "目标已存在（--force 可先改名保留）：$dst"
      fi
    fi
    plan "cp -a $src -> $dst"
    if [ "$APPLY" = 1 ]; then
      mkdir -p "$(dirname "$dst")"
      cp -a "$src" "$dst"
    fi
  done
fi

if [ "$APPLY" = 0 ]; then
  log "Dry Run 结束：以上为恢复计划。确认后加 --apply 正式执行。"
else
  log "恢复完成。若恢复了 hideck.db，请以停止状态替换后启动容器，并核对 /ping 与设备状态。"
fi
