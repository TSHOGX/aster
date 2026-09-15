# Harness Release 与本地部署

这是通用操作手册，所有路径、账户和服务名均为虚构示例。真实部署配置、机器盘点、备份和操作日志保存在仓库之外，参见 [公开/私有数据边界](privacy.md)。

## 1. 制品与部署分离

`harness-deploy` 是 Harness 的版本交付工具，不依赖主机名、实例注册表或固定配置目录。它包含两类操作：

- 制品操作：fetch、verify、export、import、stage，只依赖版本、仓库和存储路径，不读取部署配置或调用 systemd。
- 本地部署操作：inspect、check、install、upgrade、resume、rollback、status，显式通过 `--config PATH` 选择私有部署配置。

工具仍理解 Harness 的前后端、SQLite 数据库与 runsc 生命周期；本轮不把它扩展为任意软件的通用部署框架。

发布目标为 Linux amd64 / Ubuntu 22.04、24.04 / systemd。前端包包含 standalone 和固定 Node；sandbox 以 GHCR digest 引用。现网无需 Git、Go、npm、Python、Docker、gh 或 oras。宿主准备 root 权限、systemd、nginx、runsc、ip、nft、runuser 和支持 ACL/xattr 的 GNU tar。

## 2. 获取、验证与离线分发

```bash
harness-deploy fetch --repository example-org/example-repo --release vX.Y.Z --cache /var/cache/harness/releases
harness-deploy verify --release vX.Y.Z --cache /var/cache/harness/releases
harness-deploy export --release vX.Y.Z --cache /var/cache/harness/releases --output /secure/release.tar.gz
harness-deploy import --archive /media/release.tar.gz --manifest-sha256 <trusted-sha256> --cache /var/cache/harness/releases
sudo harness-deploy stage --release vX.Y.Z --cache /var/cache/harness/releases --releases /opt/harness/releases --rootfs /opt/harness/rootfs
```

每个仓库使用独立缓存目录。版本必须精确指定，不能使用 latest。export 可在缓存缺失时通过 `--repository` 获取制品；verify 和 stage 仅使用已有缓存，import 完全离线。stage 需要 root 以保留 rootfs 所有权，但不会读部署配置、停止服务或切换 current。

完整 Release 的应用与 sandbox 附件统一通过 HARNESS_GITHUB_TOKEN 读取；仅兼容缺少 sandbox 附件的旧 Release 时，才回退到 HARNESS_GHCR_USER/HARNESS_GHCR_TOKEN。凭据注入工具进程，不能写进命令行、公开配置或日志。离线 manifest SHA256 从受信渠道取得，不能把不可信归档内部的散列当作信任根。

## 3. 准备本地配置

复制 `deploy/target.example.json` 到仓库之外，例如 `/etc/harness/deploy.json`，按实际环境填写。文件的路径和可选 label 都不决定部署身份，也不要求服务带特定实例名。配置中可以使用任何经过预检的绝对路径和 systemd 服务名。

配置需要明确：应用配置、认证文件、数据目录、缓存/展开位置、current 链接、操作记录与备份目录、服务账户/单元、健康入口、resolver、外部写者与验收钩子。所有路径必须与配置的 backup_paths 和隔离要求一致。生产认证文件必须非空有效；不以丢失凭据文件的方式进入开放访问模式。

`label` 只是可选说明，不参与锁、操作身份或恢复校验。目标身份由安装目的路径决定；操作锁同时保护目的路径和操作目录，不能通过更换 label 或操作记录目录绕过。同一数据库另有规范路径锁。

不自动扫描相邻配置或 `/etc/harness` 注册表。多套配置是否指向同一个服务、端口或数据目录，由操作者显式管理和预检；不能把“通用”理解为取消资源隔离与恢复校验。

配置的 `writers_inventoried` 默认 false；真实环境完成盘点后在私有文件中确认。配套的 `deploy/harness.example.yaml` 是合成起点，必须补齐模型、网络、数据路径和身份配置。共享 rootfs 不含生产 DNS 或私有知识，resolver 与私有 KB 在运行时挂载。

nginx 只在目标服务的 TLS server 块中加入通用示例中的维护文件检测，保留原 TLS/反代设置。工具不会改写全局 nginx 配置。维护目录须允许 nginx 读取 marker。

## 4. 配置预检和升级

```bash
sudo harness-deploy inspect --config /etc/harness/deploy.json
sudo harness-deploy check --config /etc/harness/deploy.json --release vX.Y.Z
sudo harness-deploy check --config /etc/harness/deploy.json --release vX.Y.Z --new-install
sudo harness-deploy install --config /etc/harness/deploy.json --release vX.Y.Z
sudo harness-deploy upgrade --config /etc/harness/deploy.json --release vX.Y.Z
harness-deploy status --config /etc/harness/deploy.json
sudo harness-deploy resume --config /etc/harness/deploy.json --operation <id>
```

install、upgrade、check 使用配置中指定的缓存，不会在生产变更过程中隐式下载。先获取并 stage 制品。缓存、rootfs 与备份可放在容量足够的独立文件系统；原数据库迁移的临时空间仍须单独检查。

inspect 读取 systemd MainPID 对应的 `/proc/<pid>/exe`，计算真实运行二进制 SHA256 和 Go VCS 信息，不依赖 Go 工具链或源码 HEAD。其输出含机器信息，仅供私有运维使用，不得提交或上传公共 CI。首次接管未版本化二进制需在私有配置填写 legacy_binary_sha256；已有托管 Release 会比对实际运行二进制与 current 下的制品。

旧部署接管不搬迁数据。操作者先在隔离环境审查已有 systemd 启动方式和回滚制品布局，适配为配置的 current 与 runtime.env。不同内容的已有 unit 不自动覆盖。新安装由工具创建配置指定的单元，宿主依赖和账户仍由运维准备。

部署依次记录 stage、maintenance、drain、stop、backup、migrate、switch、verify、traffic、background。维护仅挡入口；drain 阻止新 API 写入、cron 触发和 nightly，已有任务必须完成。独立 external writers 必须受已登记 cgroup 管理。超时不取消用户任务，撤销本次 drain 并记录 aborted。

三个本地钩子必须使用绝对可执行路径和 argv：

| 钩子 | 返回 0 的契约 |
|---|---|
| external-idle | 已无外部在途写者；旧后端不支持 drain 报告时，也需证实其自动任务已停止；停服务后还需确认没有脱离 cgroup 的进程 |
| cold-start | 用支持的生命周期操作释放旧 sandbox，保留会话和数据，确保不恢复不兼容的旧内存 checkpoint |
| smoke | 受控账户完成健康、历史数据、KB、真实模型和队列操作验收；失败必须非零退出 |

备份包含完整数据目录和配置、SQLite sidecars、KB、工作区等，保留 ACL/xattr。操作状态绑定 manifest、配置摘要和备份散列；resume 拒绝功能配置漂移。内部 smoke 时入口仍维护，自动任务仍 hold；恢复入口前先持久记录“可能已开放写入”。

## 5. 数据库与回滚

```bash
orchestrator db status --config /private/harness.yaml
orchestrator db plan --config /private/harness.yaml
# 停止所有写者并备份后：
orchestrator db migrate --config /private/harness.yaml
sudo harness-deploy rollback --config /etc/harness/deploy.json --operation <id>
```

数据库命令的 `--config` 指应用 YAML；部署工具的 `--config` 指部署 JSON，两者不同。

两库保持物理分离，不执行 db-split/reverse。托管服务只检查 schema，DDL 在独立迁移入口执行。迁移先识别两库结构，再对副本升级、校验、逐库替换。每库有完成记录，支持部分完成后重入；不假设跨文件事务。

旧库采用经过审查的精确 DDL 指纹。额外列/trigger、其他 ALTER 历史和未知版本可能被保守拒绝，必须在受控备份上复核，不在生产绕过检查。存在非空旧 WAL 时拒绝替换；已停写并备份后正确 checkpoint，不能直接删除非空 WAL。

回滚只在尚未开放写入且具有完整恢复点时执行，保存失败现场、在空目录恢复旧快照，避免旧 DB 混用新 WAL。恢复和 smoke 后保留维护/hold，操作者再确认放行。已开放或可能开放写入后，不自动恢复 T0 覆盖新增数据，先保存 T1 并安排向前修复或数据对账。

## 6. GitHub 构建，目标机器部署

完整构建和基础检查全部使用 GitHub 托管的临时 Ubuntu runner，不需要自托管 runner、生产 SSH 权限、资格主机或自定义 GitHub Environments。现网不承担构建工作。

可信 tag 触发：代码 CI → 应用包与 sandbox 构建 → 完整清单 → Ubuntu 22.04/24.04 制品冒烟 → GitHub Release。应用版本/commit、归档散列、携带的 Node、standalone 运行时配置、合成旧库迁移和真实 runsc 基础启动均自动检查。

CI 的数据库是仓库内的合成 schema/测试数据，sandbox 无网络、无生产凭据，不调用真实模型。公开报告仅记录这些制品检查，并明确 `production_verified=false`；不能把它当作某个现网环境已通过业务验收。

固定 Go/Node/Pi、Ubuntu snapshot、Python hashes 和用于 CI 的 runsc；不得覆盖公开资产或移动已推送 tag。rootfs 同时以 digest 放在 GHCR，并作为 Release 的 sandbox 压缩包附件提供，完整包可由任意目标机器下载。

部署预检仍检查系统、工具、空间、身份、数据库结构、备份覆盖和服务配置。若目标 runsc 与 CI 测试版本不同，显示警告，要求在该部署验证启动；不再把精确版本相同当作跨机器部署的前提。

每个现网第一次配置私有 target 文件、服务和本地检查方法，之后升级复用即可：下载 → stage → check → 排空和停写 → 成套备份 → 数据迁移 → 切换 → 业务 smoke → 恢复流量。不需要把机器注册到 GitHub，也不回传生产日志、数据库或机器信息。

真实业务验收包括模型调用、已有会话/文件/KB、Stop/队列、外部代理及定时任务。由该环境在维护窗口中完成，失败维持维护并按本手册恢复；CI 不能替代这一环。

## 7. 发版和试部署

CI 在分支 push、PR 和手动触发时运行。合入 main 本身不会部署生产；推送 `v*` tag 才构建和发布制品。

首次完整版本先发新的 `v0.3.0-rc.N`，完整流水线通过后在 GitHub Releases 下载四个组件、release-manifest.json、SHA256SUMS 和操作说明。每个目标环境先备份、升级并实际验证；通过后再发正式 `v0.3.0`。候选失败或代码变化时增加 rc 编号，保留旧 tag。

Actions artifact 可用于诊断中间构建，但完整 Release 才是常规部署输入。生产切换前的备份、排空和迁移检查仍然必要；它们是本地操作步骤，不是一套额外平台。
