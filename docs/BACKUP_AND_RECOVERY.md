# HiDeck 双轨备份与独立维护

目标：**上游 GitHub 仓库或 Docker 镜像删除后，仍能独立部署、构建、维护 HiDeck，并保持随行号（Suixinghao）兼容。**

本文件覆盖：双轨架构、首次初始化、GHCR 发布、NAS 离线备份、灾难恢复、随行号切换到自有镜像、上游消失后的维护流程、安全与许可证注意事项。

---

## 1. 双轨备份架构

| 轨道 | 内容 | 载体 | 触发方式 |
| --- | --- | --- | --- |
| A：原版保全 | 官方 `docker.io/yibaiba/hideck:2.1.23` 的**多架构 Manifest 级拷贝**（不重编译、不改内容） | `ghcr.io/zhangsan-nb/hideck:upstream-2.1.23` | 手动 `backup-upstream` 工作流 |
| B：Fork 构建 | 从自己 Fork 源码构建的镜像（可变测试 + 按 Commit 固定） | `ghcr.io/zhangsan-nb/hideck:fork-main`、`:build-<SHA>` | 手动 / `main` 源码变更 |
| C：离线归档 | Git Bundle + `docker save` 镜像 tar + 运行数据 + 校验 | NAS 本地目录（`HIDECK_BACKUP_DIR`） | 手动运行脚本（可配 cron） |

三层互相独立：GHCR 用于在线恢复；NAS 归档用于**完全离线**（上游与 GHCR 均不可用）恢复。

> 轨道 A 的镜像与官方逐字节一致（索引或各平台清单摘要一致），**不得**用 Fork 自建镜像冒充原版 2.1.23。

---

## 2. 首次初始化

1. 确认 Fork 存在：`zhangsan-nb/hideck`（main 分支）。Actions 已启用（Settings → Actions → Allow all actions）。
2. 本仓库新增：
   - `.github/workflows/backup-upstream.yml`（轨道 A）
   - `.github/workflows/build-fork.yml`（轨道 B）
   - `scripts/backup-to-nas.sh` / `scripts/restore-from-backup.sh`
   - `examples/suixinghao-hideck-compose.yml`
3. （可选，仅你本人）本地推送 GHCR 需要 `write:packages` 权限的 Token：
   ```bash
   gh auth refresh -s write:packages
   echo "$(gh auth token)" | docker login ghcr.io -u zhangsan-nb --password-stdin
   ```
   仅通过 Actions 发布时**无需**该权限（工作流内使用 `GITHUB_TOKEN`，自带 `packages: write`）。
4. （建议）GHCR 包默认是私有。保持私有即可满足个人保全；如要公开，需先阅读许可证（见 §10）。

## 3. GitHub Actions 使用说明

### 轨道 A：保全原版镜像
```bash
gh workflow run backup-upstream -f source_image=docker.io/yibaiba/hideck:2.1.23 -f target_tag=upstream-2.1.23
gh run list --workflow=backup-upstream --limit 3
gh run view <run-id>            # 查看结果与步骤日志
```
- 源镜像不存在时**直接失败**并提示，不会用其它版本顶替。
- 目标标签已存在时：摘要一致 → 跳过（幂等）；摘要不同 → 默认**拒绝覆盖**，除非显式 `force=true`。
- 运行完成后：步骤摘要与 `upstream-preserve-evidence` 产物中记录**源/目标索引摘要**；多架构时逐平台比对清单摘要。

### 轨道 B：Fork 构建
```bash
gh workflow run build-fork                          # 默认仅 linux/amd64
gh workflow run build-fork -f platforms=linux/amd64,linux/arm64   # 源码与依赖支持时
```
- `main` 上源码路径变更会自动触发（见工作流 `paths` 过滤）。
- 标签：`:fork-main`（可变，仅测试）与 `:build-<完整SHA>`（固定，生产用）。
- 构建失败不会推送任何镜像；成功后摘要里给出 `Digest`。

## 4. 如何验证镜像 Digest

```bash
# 上游与 GHCR 目标对比（多架构索引摘要 + 各平台清单摘要）
docker buildx imagetools inspect docker.io/yibaiba/hideck:2.1.23
docker buildx imagetools inspect ghcr.io/zhangsan-nb/hideck:upstream-2.1.23

# 本地镜像
docker images --digests | grep hideck
docker image inspect yibaiba/hideck:2.1.23 --format '{{json .RepoDigests}}'
```
判定标准：索引摘要一致，或（打包器重封装时）**各平台清单摘要逐项一致**。不一致即为保全失败，必须书面记录并停止使用该镜像。

## 5. NAS 离线备份

```bash
export HIDECK_BACKUP_DIR=/vol1/1000/docker/hideck-backup   # 飞牛示例
export HIDECK_RUNTIME_DIR=/vol1/1000/docker/hideck         # 现有运行目录（config/、data/）
export HIDECK_FORK_IMAGE=ghcr.io/zhangsan-nb/hideck:build-<SHA>  # 可选
./scripts/backup-to-nas.sh
```

- 目录结构（每次备份一个时间戳目录，`latest` 指向最新）：
  ```
  hideck-backup/
    backup-<ts>/
      source/  images/  deploy/  runtime/  manifests/  checksums/
    logs/        # 每次运行的完整日志
    latest -> backup-<ts>
  ```
- **原子性**：先在隐藏临时目录生成 → `sha256sum -c` 全量校验 → 才移动到正式目录；**绝不覆盖历史备份**。
- SQLite 数据使用在线备份 API（运行中一致），**不停止容器**。
- `HIDECK_RETENTION=N` 才会按数量删除旧备份；默认 0 = 永不删除。
- `HIDECK_GPG_RECIPIENT=<key>` 时对 config/db 进行 GPG 加密。
- 空间不足、镜像拉取失败等都会给出明确错误并非零退出（原版镜像归档失败=直接终止）。

### 自动备份（可选，自行决定后再启用）
```cron
# 每周日 04:30
30 4 * * 0 HIDECK_BACKUP_DIR=/vol1/1000/docker/hideck-backup HIDECK_RUNTIME_DIR=/vol1/1000/docker/hideck /path/scripts/backup-to-nas.sh >> /vol1/1000/docker/hideck-backup/logs/cron.log 2>&1
```

## 6. 灾难恢复

`scripts/restore-from-backup.sh`：**默认 Dry Run**，`--apply` 才写入；任何时候先做 SHA256 校验；不覆盖已有文件（`--force` 时先把旧文件改名为 `*.pre-restore-<ts>`）；不自动停容器。

```bash
# 1) 预演（打印计划，不写盘）
./scripts/restore-from-backup.sh --backup-dir /…/hideck-backup/backup-<ts> \
  --load-images --restore-git /tmp/hideck-src --restore-deploy /tmp/hideck-deploy --restore-runtime /tmp/hideck-runtime

# 2) 正式执行
./scripts/restore-from-backup.sh --backup-dir /…/backup-<ts> --apply …（同上参数）

# 3) 离线恢复镜像（等价手工命令）
docker load -i backup-<ts>/images/upstream-2.1.23.tar

# 4) 恢复源码
git clone backup-<ts>/source/hideck-fork.bundle hideck-restored

# 5) 恢复 HiDeck 数据（先停容器，替换后启动）
#    docker stop hideck
#    cp -a runtime/hideck/hideck.db   <数据目录>/data/hideck.db
#    cp -a runtime/hideck/config.yaml <数据目录>/config/config.yaml
#    docker start hideck && curl -s http://127.0.0.1:7577/ping
```
全部步骤均可在**无外网**环境完成。

## 7. 让随行号使用自己的 GHCR 镜像

随行号官方部署固定 `yibaiba/hideck:2.1.23`（`deploy/unraid/docker-compose.yml`）。

切换步骤（**只改 image 一行，其余保持不动**）：
1. 备份现有 compose：`cp docker-compose.yml docker-compose.yml.bak-<ts>`
2. `image: ghcr.io/zhangsan-nb/hideck:upstream-2.1.23`（示例见 `examples/suixinghao-hideck-compose.yml`）
3. 生产建议固定 Digest：`ghcr.io/zhangsan-nb/hideck@sha256:<DIGEST>`
4. 保持：`network_mode: host`、`privileged: true`、`/dev:/dev` 透传、config/data/logs 卷、`CONFIG_PATH`、健康检查 `127.0.0.1:7577/ping`
5. 不要修改设备映射与 HiDeck 数据目录（否则可能丢短信/策略数据）
6. 验收：`docker compose config` → 重建容器 → `/ping` → 随行号 `/voice-test/health` → 设备在线与 CLCC

> 若 GHCR 包为私有：随行号所在宿主需先 `docker login ghcr.io`（用带 `read:packages` 的 Token）。

## 8. 上游删除后的独立维护流程

1. 源码：Fork + NAS 上的 `hideck-fork.bundle`（完整历史与 Tags）已可独立工作。
2. 镜像：`upstream-2.1.23`（保全）+ `build-<SHA>`（自建）双保险。
3. 日常修复：在 Fork 上改动 → `build-fork` 出 `build-<SHA>` → 随行号改固定 Digest 上线。
4. 若需吸收上游后续版本：`git remote add upstream https://github.com/yibaiba/hideck` 已不可用时，用旧 bundle 或镜像内源码延续维护；新版本在 Fork 上以 Tag 发布。
5. 版本命名：自建镜像一律带 `fork-`/`build-` 前缀，**不得**命名/宣称成官方 `2.1.23`。

## 9. 测试与验收清单

- [ ] `bash -n scripts/*.sh`、`shellcheck`（如可用）
- [ ] 工作流 YAML 解析（`python3 -c 'import yaml,sys;yaml.safe_load(open(f))'`）
- [ ] `docker compose -f examples/suixinghao-hideck-compose.yml config`
- [ ] `backup-upstream` 实跑一次并记录源/目标 Digest
- [ ] `build-fork` 实跑一次并记录 Digest
- [ ] NAS 实跑一次备份：`sha256sum -c checksums/SHA256SUMS` 通过、`docker load` 可识别镜像
- [ ] `git bundle verify` 通过、`restore-from-backup.sh` Dry Run 输出合理

未实际执行的条目必须标记为"未验证"。

## 10. 安全与许可证注意事项

- HiDeck 上游代码为 **PolyForm Noncommercial 1.0.0**（非商业限制），且仓库含 `third_party/vowifi-go`（AGPL-3.0）等第三方组件：个人/非商业自用与备份没有问题；**公开分发镜像前**必须逐项核对许可证义务（含 AGPL 源码提供义务），不确定时保持 GHCR 包私有。
- 本备份体系中的镜像**只做原样拷贝**，不改内容、不冒充官方；Fork 构建镜像必须使用 `fork-`/`build-` 标签并保留来源说明。
- 禁止把密钥、Token、数据库、短信记录提交进 Git；`runtime/` 备份包含敏感数据，注意加密与权限（脚本默认 0600）。
- GHCR 私有包建议仅给运行宿主最小化的 `read:packages` 凭据。
- 自动化外部操作（推送 GHCR、写 NAS）前先核对目标与权限；**不要**用工作流覆盖已确认的原版保全标签（除显式 `force=true`）。
