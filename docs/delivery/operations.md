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

私有制品读取凭据通过 HARNESS_GITHUB_TOKEN 和 HARNESS_GHCR_USER/HARNESS_GHCR_TOKEN 注入工具进程，不能写进命令行、公开配置或日志。离线 manifest SHA256 从受信渠道取得，不能把不可信归档内部的散列当作信任根。

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

## 6. 构建和资格验收

可信 tag 经精确源码检查、可复用 CI、应用/rootfs 构建、隔离 Ubuntu 22.04/24.04 资格验收后才能公开 Release。固定 Go/Node/Pi、Ubuntu snapshot 和 Python hashes；不得覆盖已公开资产、重建覆盖同名 tag 或自动清理被引用的 rootfs digest。

发布构建只复制显式列举的公开文件，不复制整个 deploy/docs 目录。操作日志、机器盘点、配置和备份不属于制品。

资格主机由运维提供 root 管理的 `/etc/harness-release/qualify` 适配器，接收候选目录、平台和私有报告路径，执行真实验证。原始报告与 stdout/stderr 留在私有位置；sanitizer 只输出与 manifest 匹配的版本、平台、runsc 和固定测试项的通过状态。额外机器字段不会上传，publisher 会拒绝未经净化的报告。

构建/资格 runner 必须与生产隔离；配置 Actions/GHCR 只需构建发布权限，不给予生产访问。缺 runner、钩子或有效报告即阻塞发布，不生成固定 PASS 代替验收。

正式生产推广前，在私有运维系统完成真实备份预演和分批升级记录。源代码测试、公共软件版本矩阵与特定机器的资格证据必须区分。
