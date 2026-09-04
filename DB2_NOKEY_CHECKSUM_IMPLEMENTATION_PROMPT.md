# DB2 无唯一键表 checksum-only 实现提示词

请在现有 `sync-diff-standalone` 仓库中实现 DB2 → TiDB 无主键/无唯一键表的安全对比降级模式。

## 工作位置与基线

- Windows：`D:\codex-WorkSpace\sync-diff-standalone`
- WSL：`/mnt/d/codex-WorkSpace/sync-diff-standalone`
- 分支：`feature/db2-source`
- 实现代码基线：`a05bf6704fa83a81a4fbd07bb4b0eefa8465e41c`
- 远端分支：`origin/feature/db2-source`

远端分支会比实现代码基线多一个只增加本提示词的文档提交；以包含本文件的最新 `origin/feature/db2-source` 为实际起点。

开始前必须执行并汇报：

```powershell
pwd
git branch --show-current
git status --short
git rev-parse HEAD
```

如果实现代码基线不在当前历史中，或工作树已有修改，先审查并保留现有内容，不得重置、覆盖或回滚用户文件。

## 先阅读

- `DB2_SOURCE_IMPLEMENTATION_HANDOFF.md`
- `DB2_SOURCE_IMPLEMENTATION_EVIDENCE.md`
- `sync_diff_inspector/DB2_SOURCE.md`
- `sync_diff_inspector/config/config.go`
- `sync_diff_inspector/source/common/table_diff.go`
- `sync_diff_inspector/source/db2.go`
- `sync_diff_inspector/source/tidb.go`
- `sync_diff_inspector/source/canonical.go`
- `sync_diff_inspector/source/source.go`
- `sync_diff_inspector/canonical/v1.go`
- `sync_diff_inspector/diff.go`
- `sync_diff_inspector/report/report.go`
- `sync_diff_inspector/checkpoints/checkpoints.go`

不要从旧计划重新实现 DB2Source。现有有键表的 keyset 分块、`CanonicalV1`、GBK 解码、差异行定位、修复 SQL、checkpoint 和进度修复都必须保留。

## 背景与问题

当前 DB2Source 只接受 DB2 与 TiDB 两端共同存在、非空且已声明为 PRIMARY KEY/UNIQUE KEY 的排序键。无共同唯一键的表会在初始化时报错，例如：

```text
db2 ordering key for <schema>.<table> is not a declared primary or unique key in Db2
```

不能直接照搬 MySQL/TiDB 的“按所有列排序后逐行比对”：DB2 与 TiDB 的排序规则、字符编码、NULL 顺序和类型语义可能不同；非唯一边界还可能造成漏行或重复行。目标是增加一种显式启用的、整表流式、与行顺序无关的校验模式，只回答“数据集合相同/不同”，不定位具体差异行。

## 必须实现的行为

### 1. 显式配置，默认仍严格报错

在 DB2 数据源配置中增加：

```toml
[data-sources.db2_source]
type = "db2"
no-unique-key-mode = "checksum-only"
```

要求：

- 默认值为 `error`，保持现有兼容和安全行为。
- 仅允许 `error` 与 `checksum-only`；未知值启动时报清晰错误。
- 该配置只对 DB2 上游生效，不改变 MySQL/TiDB 原有路径。
- 如果用户显式配置了 `index-fields`，字段不存在、可空或不是两端共同声明的唯一键时仍必须报错，不能静默退化，以免掩盖配置拼写错误。
- 只有未显式配置 `index-fields`、且遍历所有候选后找不到共同稳定唯一键时，才允许进入 `checksum-only`。

### 2. 先修正自动键选择

当前实现倾向于先选 TiDB 的一个主键/唯一键，再检查 DB2 是否相同。改为：

- 按“主键优先、再唯一键”的稳定顺序枚举 TiDB 全部非空候选键。
- 对每个候选键检查 DB2 是否存在同列、同顺序、非空的 PRIMARY/UNIQUE KEY。
- 选择第一个两端共同候选键，继续使用现有 keyset 路径。
- 只有全部候选都不匹配时才报错或进入 `checksum-only`。
- 不得因为第一个 TiDB 候选键不匹配，就忽略后续可能匹配的唯一键。

### 3. 增加运行时表模式

给 `common.TableDiff` 增加仅运行时、不序列化的明确模式，至少区分：

- `keyset`：现有有键行为；
- `unordered-checksum-only`：无共同唯一键的整表无序校验。

不要通过 `OrderKeyColumns == nil` 到处隐式猜测模式。初始化时记录选择结果，并输出一次清晰警告：该表只做整表 checksum，无法定位差异行，也不会生成修复 SQL。

### 4. 整表单块、流式读取、禁止 ORDER BY

对 `unordered-checksum-only`：

- splitter 只生成一个覆盖整表的 chunk；无 keyset bounds、无 `ORDER BY`。
- DB2 和 TiDB 两端都使用无 `ORDER BY` 的流式 `SELECT`。
- 只能保持当前行和固定大小摘要状态，额外内存必须为 O(1)；禁止把整表载入内存，禁止外部排序。
- 保留已有列映射、忽略列、类型规范化、DB2 GBK 解码和 `CanonicalV1` 行编码语义。
- 如果任务中同时有有键表和无键表，两种模式必须能混合工作，且有键表行为完全不变。

### 5. 新增顺序无关、重复敏感的摘要协议

在 `sync_diff_inspector/canonical/` 中增加独立、版本化算法，例如 `CanonicalMultisetV1`。不要修改现有 `CanonicalV1` 的结果。

建议协议（若采用等价方案，必须解释并测试同等性质）：

1. 每行继续调用现有 `EncodeRow`，复用其 NULL、空字符串、整数、decimal、浮点、日期时间、二进制、LOB 和字符规范化。
2. 对编码行计算两个带不同 domain separator 的 SHA-256：`h1(row)`、`h2(row)`。
3. 分别按无符号 256 位整数模 `2^256` 累加所有 `h1` 和 `h2`，同时累计 `rowCount`；可再维护一个 XOR 聚合，但 XOR 不能作为唯一聚合，因为偶数个重复行会抵消。
4. 最终摘要为 `SHA-256(version || rowCount || sum1 || sum2 [|| xor])`，所有整数使用固定宽度、固定字节序。

必须满足：

- 行顺序变化摘要不变；
- 重复行数量变化摘要改变；
- NULL 与空字符串不同；
- 不同逻辑类型不混淆；
- O(1) 额外内存；
- `ChecksumInfo.Algorithm` 明确为新版本，禁止与 `CanonicalV1` 或旧 MySQL checksum 跨算法判等。

文档必须说明这是低碰撞概率的集合摘要，不是数学上的零碰撞证明。

### 6. 不同数据时只报告“不相等”

当 `unordered-checksum-only` 的行数或摘要不同时：

- 直接把表标记为数据不相等；
- 明确输出“checksum mismatch，差异行明细不可用”；
- 禁止调用 `BinGenerate`；
- 禁止调用 `compareRows`；
- 禁止生成该表的 INSERT/DELETE/REPLACE 修复 SQL，即使全局 `export-fix-sql = true`；
- 不得虚构 `rowAdd = 0`、`rowDelete = 0` 就代表没有增删。报告层应表达“未知/不可定位”，同时不破坏现有报告格式与有键表行为。

当摘要相等时，报告该表数据相等，并标明判定方式为 `CanonicalMultisetV1`。

### 7. checkpoint 和进度边界

- 无键表只有一个整表 chunk，进度应显示为一个任务，不得出现 `N/0` 或错误百分比。
- 本阶段不实现整表扫描中途续传。若扫描被中断，下次从该表开头重扫。
- 不得宣称支持无键表的行级 checkpoint resume。
- 现有有键表 typed keyset checkpoint 行为不能退化。

## 离线测试要求

至少补齐以下确定性测试，不连接任何数据库：

1. 配置默认 `error`、显式 `checksum-only`、非法值拒绝。
2. 显式无效 `index-fields` 即使启用 fallback 仍报错。
3. 第一个 TiDB 唯一键不匹配 DB2、第二个共同唯一键能被选中，不应 fallback。
4. 无共同键时默认报错；显式启用时进入 `unordered-checksum-only`。
5. 多种行排列得到相同 multiset digest。
6. 重复行数量变化、同总行数但内容变化得到不同 digest。
7. 空表、单行、NULL/空字符串、中文、GBK 解码后文本、binary、decimal、date/time/timestamp、LOB 边界。
8. DB2 与 TiDB 无键整表查询都不含 `ORDER BY`，且只产生一个 chunk。
9. checksum-only 不相等时不进入 `BinGenerate`、不进行第二阶段逐行比较、不生成修复 SQL。
10. checksum-only 相等时正确完成报告。
11. 同一任务混合有键表和无键表。
12. 现有 DB2 keyset、checkpoint、差异定位、修复 SQL以及 MySQL/TiDB 回归测试全部通过。

优先使用 `sqlmock`、内存迭代器和 mock Source。测试与文档中不得写入真实表名、地址、账号、密码或用户日志里的生产数据。

## 权限与安全边界

1. 禁止连接 DB2、TiDB、PD 或 etcd。
2. 禁止执行 `scripts/run-db2-local.ps1 -Action Run`。
3. 可以运行离线 Go 测试和 `scripts/run-db2-local.ps1 -Action Build`。
4. 禁止读取、修改、输出或提交 `sync_diff_inspector/config/config_db2.local.toml` 中的凭据。
5. 不修改 `master/main`，不合并分支，不重写历史。
6. 可以在 `feature/db2-source` 上按职责创建本地提交；除非用户另外明确要求，否则不要推送远端。
7. 保留用户已有修改。临时文件、缓存和探测文件完成后必须删除。
8. 不要把“离线测试通过”描述成“真实数据库验证通过”或“生产可用”。

## 文档更新

更新以下文档，保证配置、限制和用户验收步骤一致：

- `sync_diff_inspector/DB2_SOURCE.md`
- `sync_diff_inspector/config/config_db2.toml`
- `DB2_SOURCE_IMPLEMENTATION_HANDOFF.md`
- `DB2_SOURCE_IMPLEMENTATION_EVIDENCE.md`

示例中说明：

- 有共同唯一键时仍优先 keyset，可定位差异并生成修复 SQL；
- 无共同唯一键且显式启用时，只能判定整表相等/不相等；
- 无键大表每次比较需要两端完整扫描，成本高；
- 真实数据库验收必须由用户本人执行。

## 离线验收命令

先运行针对性测试，再运行完整离线测试；所有命令必须等待明确退出码：

```powershell
$env:SYNC_DIFF_RUN_INTEGRATION = '0'
go test ./sync_diff_inspector/canonical ./sync_diff_inspector/config ./sync_diff_inspector/source ./sync_diff_inspector/report ./sync_diff_inspector/checkpoints ./sync_diff_inspector -count=1
go test ./sync_diff_inspector/... -count=1
.\scripts\run-db2-local.ps1 -Action Build
git diff --check
git status --short
```

如果在 WSL 中工作：

- 可以运行 Linux 离线测试；
- Windows 原生 `db2cli` Build 不能用 Linux 测试或交叉编译替代；
- 无法执行 Windows Build 时，必须明确列为“本轮未验证”，交给用户回到 PowerShell 执行，禁止伪造成功。

## 完成标准与最终汇报

完成后按职责拆分本地提交并保持工作树干净。最终必须汇报：

- 分支、起始 SHA、全部新 commit SHA；
- 配置示例和默认行为；
- 自动共同唯一键选择的变化；
- multiset 摘要协议及其顺序无关、重复敏感、O(1) 属性；
- checksum-only 不相等时为何不提供差异行和修复 SQL；
- 每条测试/Build 命令的真实退出码；
- 已验证、未验证、需要用户执行的真实数据库验收；
- 明确声明未连接数据库、未执行 `-Action Run`、未读取或泄露本地配置凭据、未推送远端。

不要以“代码已写完”作为结束条件；必须完成离线测试、文档、提交和证据分级。真实 DB2/TiDB 验收仍由用户在 PowerShell 中另行执行。
