# DB2 源端缺表与对象解析修复提示词

请在现有 `sync-diff-standalone` 仓库中修复 DB2 上游初始化时对“目标表存在、DB2 源表不存在或无法按当前标识符找到”的处理。

## 工作位置与基线

- Windows：`D:\codex-WorkSpace\sync-diff-standalone`
- WSL：`/mnt/d/codex-WorkSpace/sync-diff-standalone`
- 分支：`feature/db2-source`
- 实现代码基线：`23226e9a0580fc859c1a65173ea71896ac05c27e`
- 当前远端仍落后于本地；不得重置、覆盖或丢弃已有三个本地提交。

以包含本提示词的最新本地 `feature/db2-source` 为实际起点。开始前执行并汇报：

```powershell
pwd
git branch --show-current
git status --short
git log -5 --oneline --decorate
```

如果工作树已有其他修改，先审查并保留，不得回滚用户文件。

## 用户实际错误

用户在 PowerShell 中运行：

```powershell
.\scripts\run-db2-local.ps1 -Action Run
```

初始化阶段报错：

```text
from upstream: db2 table ODSRUN.TMP_BATCH_FDKFH_PRE_MAIN not found or has no columns
```

不要假设该对象必然应该存在，也不要把所有 DB2 元数据错误都吞掉。必须区分以下情况：

1. DB2 源端确实没有该对象；
2. 配置 schema 错误；
3. DB2 使用带引号的混合大小写对象名，而程序按未引用标识符转成了大写；
4. 对象是普通 VIEW、ALIAS、NICKNAME 或临时对象；
5. catalog 查询本身失败、权限不足或连接异常；
6. 目标过滤范围意外包含了只存在于 TiDB 的表。

## 先阅读

- `sync_diff_inspector/source/source.go`
- `sync_diff_inspector/source/db2.go`
- `sync_diff_inspector/source/mysql_shard.go`
- `sync_diff_inspector/source/tidb.go`
- `sync_diff_inspector/source/common/table_diff.go`
- `sync_diff_inspector/db2util/metadata.go`
- `sync_diff_inspector/db2util/conn.go`
- `sync_diff_inspector/diff.go`
- `sync_diff_inspector/config/config.go`
- `sync_diff_inspector/DB2_SOURCE.md`
- `DB2_SOURCE_IMPLEMENTATION_HANDOFF.md`
- `DB2_SOURCE_IMPLEMENTATION_EVIDENCE.md`

当前关键缺口：`buildSourceFromCfg` 已收到 `skipNonExistingTable`，但 DB2 分支调用 `NewDB2Source(ctx, tableDiffs, dbs[0])` 时丢弃了该参数。`NewDB2Source` 又把目标端生成的 `tableDiffs` 全部当成两端都存在，直接调用 `db2util.ReadTableInfo`。因此 DB2 没有对应表时，还没有机会设置 `TableLack = UpstreamTableLackFlag`。

## 必须实现的行为

### 1. 为 DB2 接通 skip-non-existing-table

将 `skipNonExistingTable` 明确传入 DB2 构造路径，不要使用全局变量，也不要解析错误字符串。

行为必须与已有 MySQL/TiDB 语义一致：

- `skip-non-existing-table = false`：DB2 源对象不存在时初始化失败，但错误必须明确给出规范化后的 schema/table，并提示检查源端对象、带引号大小写、过滤范围或启用 `skip-non-existing-table`。
- `skip-non-existing-table = true`：将对应 `TableDiff.TableLack` 设置为 `common.UpstreamTableLackFlag`，跳过 DB2 列映射、键选择、checksum-only 和数据扫描，继续初始化其他表。
- 缺表不是“空表”，不得为缺表计算空集合 checksum，也不得将其错误报告为数据相等。
- 不允许 `no-unique-key-mode = "checksum-only"` 掩盖缺表；checksum-only 只处理两端表都存在但没有共同唯一键的情况。

### 2. 使用类型化的缺表判定

不要通过匹配 `"not found or has no columns"` 文本判断缺表。选择一种清晰方案，例如：

- 在 `db2util` 定义可被 `errors.Cause`/`errors.As` 识别的 `TableNotFoundError`；或
- 先使用集中封装的 catalog 对象存在性查询，再读取列元数据。

要求：

- catalog 返回零列可以归类为“对象不存在或当前对象类型不可读取”；
- SQL 执行失败、权限不足、驱动错误、连接错误必须原样返回，不能当成缺表跳过；
- DB2 表即使数据行为 0 行，`SYSCAT.COLUMNS` 仍有列，绝不能被当成缺表。

### 3. 标识符与对象类型

- 保留现有 DB2 未引用标识符转大写规则。
- 带双引号的 schema/table 必须保留精确大小写；增加离线测试覆盖 `"MixedCase"`。
- 禁止大小写不敏感地随机选择同名 DB2 对象，避免比较错误表。
- 普通 VIEW 如果能从 catalog 读取完整列并能正常 SELECT，可以按只读对象继续比较；无共同键时只能走 checksum-only。
- 对 ALIAS、NICKNAME、声明的临时表或其他特殊对象，要么以明确测试证明支持，要么给出带对象类型的可操作错误。不要笼统宣称全部支持。
- 如果需要新增 catalog 对象查询，将 SQL 集中放在 `sync_diff_inspector/db2util/metadata.go`，不要散落到 source 层。

### 4. 报告与任务行为

源端缺表且允许跳过时，复用现有 `TableLack` 流程，使终端和 summary 明确显示该表在 upstream 不存在。不得：

- 生成修复 SQL；
- 进入结构比较、分块、checksum 或逐行比较；
- 把该表计为“数据相等”；
- 因一张允许跳过的缺表阻止其他正常表继续比较。

如果所有目标表在 DB2 都不存在，保留现有任务语义并给出清晰结果，不得 panic 或产生空 map 下标错误。

### 5. 不要采用的错误修复

- 不要简单删除 `ReadTableInfo` 的错误。
- 不要看到零列就创建一个空 `TableInfo`。
- 不要让 checksum-only 把不存在的表当成空表。
- 不要默认跳过缺表；必须服从 `skip-non-existing-table`。
- 不要通过模糊大小写匹配选择 DB2 对象。
- 不要只修改错误文案而不接通 `TableLack` 流程。
- 不要通过缩小用户的 `target-check-tables` 掩盖实现问题。

## 离线测试要求

使用 `sqlmock` 和现有 mock Source，至少覆盖：

1. DB2 catalog 返回零列，`skip-non-existing-table = false` 时返回可操作的缺表错误。
2. 同样场景在 `true` 时设置 `UpstreamTableLackFlag` 并继续初始化下一张存在的表。
3. catalog SQL 本身报错时，即使 skip 为 true 也必须返回原错误。
4. 数据为 0 行但 catalog 有列的真实空表正常初始化。
5. 普通大写未引用标识符仍按现有规则工作。
6. `"MixedCase"` schema/table 精确保留并使用精确参数查询 catalog。
7. 普通 VIEW 的策略有明确测试；不支持的特殊对象返回带类型的错误。
8. 缺表不能进入 keyset 或 `CanonicalMultisetV1`。
9. `skip-non-existing-table = true` 的混合任务中，缺表被报告，其他有键表和 checksum-only 表仍能继续。
10. 所有表都缺失时无 panic、无越界、结果可解释。
11. 现有 DB2 keyset、无键 checksum-only、MySQL/TiDB 和报告测试全部通过。

测试中使用虚构 schema/table，不得写入用户实际表名、地址或凭据。

## 配置与文档

在 DB2 文档和示例中说明根级配置：

```toml
skip-non-existing-table = true
```

并说明：

- `false` 是严格模式，发现目标表在 DB2 不存在即失败；
- `true` 只跳过确实不存在的 DB2 源对象；
- 该选项不修复 schema、大小写或权限配置错误；
- `no-unique-key-mode` 与缺表处理是两个不同问题。

更新：

- `sync_diff_inspector/DB2_SOURCE.md`
- `sync_diff_inspector/config/config_db2.toml`
- `DB2_SOURCE_IMPLEMENTATION_HANDOFF.md`
- `DB2_SOURCE_IMPLEMENTATION_EVIDENCE.md`

## 权限和安全边界

1. 禁止连接 DB2、TiDB、PD 或 etcd。
2. 禁止执行 `scripts/run-db2-local.ps1 -Action Run`。
3. 禁止读取、输出或提交 `sync_diff_inspector/config/config_db2.local.toml` 的凭据。
4. 可以运行离线 Go 测试和 `scripts/run-db2-local.ps1 -Action Build`。
5. 不修改 `master/main`，不重写历史，不回滚当前三个本地提交。
6. 可以创建本地提交；除非用户另外要求，不要推送远端。
7. 临时文件和缓存探测完成后必须删除。
8. 不得把离线测试通过描述成真实 DB2/TiDB 验证通过。

## 验收命令

Windows 上优先串行执行，避免大型测试二进制并行链接耗尽页面文件：

```powershell
$env:SYNC_DIFF_RUN_INTEGRATION = '0'
$env:GOMAXPROCS = '2'
go test -p 1 ./sync_diff_inspector/db2util ./sync_diff_inspector/source ./sync_diff_inspector/report ./sync_diff_inspector -count=1
go test -p 1 ./sync_diff_inspector/... -count=1
.\scripts\run-db2-local.ps1 -Action Build
git diff --check
git status --short
```

如果在 WSL 中工作，Linux 离线测试不能替代 Windows 原生 Build；必须把未执行的 Windows Build 如实交给用户。

## 最终汇报

完成后按职责创建本地提交并保持工作树干净。最终汇报：

- 起始 SHA 和全部新 commit SHA；
- 缺表的类型化判定方式；
- `skip-non-existing-table` 在 DB2 路径中的新行为；
- quoted identifier 和 VIEW/特殊对象的处理边界；
- 每条离线测试和 Build 命令的真实退出码；
- 哪些情况仍需用户检查 schema、对象类型和大小写；
- 用户真实验收时，严格模式和跳过模式各应看到什么结果；
- 明确声明未连接数据库、未执行 `-Action Run`、未读取本地凭据、未推送远端。

真实数据库验收必须由用户本人完成。不要把本次错误未经证据地直接归因于程序、配置或数据库中的任意一方。
