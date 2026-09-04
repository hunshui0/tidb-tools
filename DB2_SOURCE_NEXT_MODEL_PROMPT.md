# DB2Source 下一阶段模型提示词

请在现有仓库中继续收尾 DB2 → TiDB 对比功能：

- Windows 路径：`D:\codex-WorkSpace\sync-diff-standalone`
- WSL 路径：`/mnt/d/codex-WorkSpace/sync-diff-standalone`
- 当前分支：`feature/db2-source`
- 起始提交：`14b666f02ecb78cee0c471978f64342bdba4691b`

先阅读以下文件和当前实现，不要从旧计划重新实现：

- `DB2_SOURCE_IMPLEMENTATION_HANDOFF.md`
- `DB2_SOURCE_IMPLEMENTATION_EVIDENCE.md`
- `sync_diff_inspector/source/db2.go`
- `sync_diff_inspector/source/tidb.go`
- `sync_diff_inspector/diff.go`
- `sync_diff_inspector/checkpoints/checkpoints.go`
- `sync_diff_inspector/report/report.go`
- `sync_diff_inspector/progress/progress.go`
- `sync_diff_inspector/config/config_db2.toml`

## 已有证据

不要把旧文档中的“当前多分块只有离线测试”继续当成最新状态。用户已经在 Windows 上亲自完成真实 DB2 LUW → TiDB 运行：

- Windows `db2cli` 版本 Build 成功，不需要 GCC；
- DB2 被正确选为 keyset 分块来源；
- 表配置通过 `target-configs` 生效，`chunk-size = 2`；
- 实际生成并处理了 108 个连续的单列 keyset 分块；
- 使用稳定非空键完成 DB2 和 TiDB 两端同范围读取；
- 真实运行定位到 1 条字段差异；
- 运行没有 `range requires at least one ordering column`、checksum 失败或比较过程错误；
- 完整运行约 18 秒。

真实表名、键值、字段值、连接地址和凭据属于本地生产信息。只允许从用户已经生成的本地日志读取用于核对，禁止把这些值写入提交、测试、文档或最终报告。

## 权限边界

1. 不连接 DB2、TiDB、PD 或 etcd。
2. 不执行 `scripts/run-db2-local.ps1 -Action Run`。
3. 可以运行离线测试以及 `scripts/run-db2-local.ps1 -Action Build`。
4. 不修改或提交 `sync_diff_inspector/config/config_db2.local.toml`，不得输出其中的凭据。
5. 不推送远端，不合并分支，不改 `master/main`。
6. 保留用户已有修改；临时缓存和探测文件完成后必须删除。

## 本轮目标

按优先级完成以下收尾，不要扩大为新的数据库框架重构。

### P0：修正证据与交接文档

- 更新 `DB2_SOURCE_IMPLEMENTATION_EVIDENCE.md` 和交接状态，记录上述真实多分块证据。
- 严格区分：已真实验证的单表、单列稳定键、GBK 中文差异定位，与尚未真实验证的复合键、更多类型、checkpoint 中断恢复、修复 SQL执行、性能和生产可用性。
- 不得宣称整个 DB2Source 已生产可用。

### P1：修复确定存在的可用性问题

1. 当前 DB2 流式分块的 `ChunkCnt` 为 0，终端显示 `Progress 0% 108/0`。为未知总数提供正确的“已处理 N 块”或不定进度显示，不得显示错误百分比或 `N/0`。不要为了提前得到总数而把所有分块加载进内存。
2. `export-fix-sql = false` 时，报告仍提示 “The patch file has been generated”。只有确实启用并生成修复 SQL 时才显示补丁路径；关闭时应明确说明仅记录差异、不导出 SQL。
3. `sync_diff_inspector/config/config_db2.toml` 定义了 `[table-configs.orders]`，但 `[task]` 没有引用 `target-configs = ["orders"]`，导致示例中的 `chunk-size` 被静默忽略。修复示例和相关文档，并增加离线配置测试，防止 DB2 示例再次失效。

### P2：补齐尚未闭环的离线证据

- 为 DB2 keyset 的未知总分块进度行为增加测试。
- 为 `export-fix-sql` 开关的报告行为增加测试。
- 为 DB2 解码后的中文、NULL、二进制、decimal、date/time/timestamp 行生成 TiDB 修复 SQL增加离线测试；只验证 SQL文本，不执行 SQL。
- 检查 checkpoint 的中断恢复测试是否真正覆盖“已完成多个 DB2 keyset 块后，从 typed upper bound 继续”，不足时补齐确定性的离线测试。真实中断恢复仍标记为待用户验证。
- 保留现有 MySQL/TiDB 行为兼容性。

## 验收要求

至少执行并记录明确退出码：

```powershell
$env:SYNC_DIFF_RUN_INTEGRATION = '0'
go test ./sync_diff_inspector/source ./sync_diff_inspector/db2util ./sync_diff_inspector/chunk ./sync_diff_inspector/splitter ./sync_diff_inspector/checkpoints ./sync_diff_inspector/config ./sync_diff_inspector/report ./sync_diff_inspector/progress ./sync_diff_inspector -count=1
.\scripts\run-db2-local.ps1 -Action Build
git diff --check
```

如果在 WSL 中工作，Windows 原生 Build 不能被 Linux 交叉编译替代；只运行可用的离线测试，并把 Windows Build 明确列为未在本轮重验，不能伪造结果。

完成后按职责拆分提交，保持工作树干净。最终汇报：

- 分支和全部新 commit SHA；
- 每个问题的修改文件和行为变化；
- 离线测试与 Windows Build 的真实结果；
- 已验证、未验证和必须由用户执行的真实验证命令；
- 明确声明未连接任何数据库、未运行 `-Action Run`、未推送远端。

用户下一轮真实验收应由用户本人执行，不要代替用户连接数据库。
