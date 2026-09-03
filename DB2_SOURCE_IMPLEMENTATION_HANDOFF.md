# DB2 → TiDB 数据对比实现交接文档

## 继续实施状态（2026-09-03）

当前分支 `feature/db2-source` 已在既有实现上补充流式 CanonicalV1 摘要、基于
稳定非空键的 Db2 keyset 分块，以及使用 `filepath` 的 Windows 安全 checkpoint
写入。相关离线测试通过，但本机没有系统 Go、PowerShell 或 Db2 CLI 头文件；本轮
使用 `/tmp` 临时 Go 仅做离线测试，未连接任何真实数据库。真实 Db2/TiDB 连接、端到端
比较、性能和恢复验收仍由用户执行，不能由 sqlmock 结果替代。

## 1. 目标

在 `feature/sync-diff-standalone` 的基础上增加 **Db2 LUW 作为上游、TiDB 作为下游** 的数据对比能力，同时保持现有 MySQL/TiDB 行为不回归。

实现顺序以正确性为先：先建立数据库方言、元数据映射、结构化分块条件和跨数据库一致的数据规范化协议，再接入 DB2Source。V1 不要求用 DB2 SQL 复刻 MySQL 的服务端 MD5 表达式；DB2 → TiDB 对比采用两端相同的 Go 侧规范化摘要与逐行比较协议。

## 2. 当前基线

- 仓库：`https://github.com/hunshui0/tidb-tools`
- 基线分支：`feature/sync-diff-standalone`
- 编写本计划时的基线提交：`79f010dcd074dd4fdf380804b57ad7f9c8feb466`
- Go module 路径仍为：`github.com/pingcap/tidb-tools`
- 当前主要实现：
  - `sync_diff_inspector/source/source.go`：Source 接口、连接初始化和 Source 工厂。
  - `sync_diff_inspector/source/tidb.go`：TiDBSource。
  - `sync_diff_inspector/source/mysql_shard.go`：MySQLSources。
  - `sync_diff_inspector/config/config.go`：DataSource 与 MySQL/TiDB 连接配置。
  - `sync_diff_inspector/source/common/`：表结构、行数据和连接公共逻辑。
  - `sync_diff_inspector/splitter/`、`sync_diff_inspector/chunk/`：分块与范围表达。
  - `sync_diff_inspector/utils/utils.go`：MySQL/TiDB 查询、校验和与修复 SQL。

实现前先执行并记录：

- `git status --short --branch`
- `git rev-parse HEAD`
- `go version`
- `go test ./sync_diff_inspector/...`

如果基线测试在本机无法完成，必须记录准确错误或阻塞点，不能把“命令已启动”当作通过。

## 3. 范围

### 包含

- Db2 LUW 单实例作为上游数据源。
- TiDB 作为下游目标。
- DB2 连接、会话初始化、schema/table/column/index 元数据读取。
- DB2 类型到内部列模型的映射。
- DB2 标识符、参数、分页、范围条件及排序 SQL 方言。
- DB2 → TiDB 的分块、摘要比较、差异行定位和 TiDB 修复 SQL 导出。
- 配置示例、单元测试、离线契约测试及真实 DB2/TiDB 验收说明。
- 原有 MySQL/TiDB 路径的回归测试。

### 不包含

- DB2 作为下游目标。
- 自动执行修复 SQL。
- DB2 分片、多 DB2 实例合并。
- DB2 for z/OS、Db2 for i 的兼容承诺。
- CDC、增量同步或持续监听。
- V1 的 DB2 服务端校验和优化、DB2 统计直方图分块或完整 SQL 语法翻译器。
- 未经真实数据验证就宣称支持所有 DB2 类型。

## 4. 必须先确认的三个问题

1. DB2 驱动：是否接受依赖本机 Db2 CLI/原生库的驱动；如果不接受，需选定可用的纯 Go 或其他驱动并记录限制。
2. 支持版本：首个验收版本建议固定为一个明确的 Db2 LUW 版本，而不是泛称“支持 DB2”。
3. 一致性：V1 是否允许要求验收期间源表停止写入。多个连接之间无法天然共享同一个数据库快照时，不得承诺强一致在线对比。

这三个问题应写入实现 PR/提交说明。没有用户答案时，采用保守默认值：单一 Db2 LUW 版本、单实例、验收期间数据静止、驱动依赖明确落文档。

## 5. 实施计划

### [ ] 阶段 0：建立分支与基线证据

- 从 `feature/sync-diff-standalone` 创建 `feature/db2-source`，不直接修改基线分支。
- 记录基线 SHA、Go 版本、现有构建/测试结果和已知 Windows 构建限制。
- 建立小步提交策略；每个阶段单独提交，避免一次提交混入连接、元数据、分块和摘要等多类变更。

验收门：分支来源明确，工作树无无关修改，基线结果可复现。

### [ ] 阶段 1：把数据库类型和连接创建从 MySQL 配置中拆开

- 在 `sync_diff_inspector/config/config.go` 为 `DataSource` 增加显式数据库类型，建议值为 `mysql`、`tidb`、`db2`；旧配置未填写时保持当前自动识别行为。
- 将 `ToDriverConfig`、`RegisterTLS`、`ConnectMySQL` 等 MySQL 专用逻辑从通用初始化路径隔离。
- 在 `sync_diff_inspector/source/source.go` 将 `initDBConn` 改为按数据源类型调用连接工厂；明确拒绝 `target-instance` 为 `db2`。
- 将 DB2 配置限制在真实需要的字段：host、port、database、user、password、schema、连接参数和必要的 TLS/证书字段。
- 密码继续使用现有脱敏类型，不把 DSN 或密码写入日志。

验收门：旧配置无需修改即可通过原有配置测试；`type = "db2"` 能进入 DB2 连接分支；DB2 作为 target 会得到清晰错误。

### [ ] 阶段 2：增加 DB2 驱动适配与会话初始化

- 新建独立包，例如 `sync_diff_inspector/db2util/`，不要把 DB2 SQL 塞进 MySQL 专用的 `pkg/dbutil`。
- 封装 DSN 创建、连接、ping、连接池大小、超时、只读事务/隔离级别和 session 初始化。
- 明确 DB2 schema 默认值、未加引号标识符转大写规则，以及 quoted identifier 的大小写保留规则。
- 对驱动错误建立最小分类：认证失败、网络失败、对象不存在、权限不足、查询取消。
- 如果驱动依赖原生库，增加构建说明和启动前检查；错误应指出缺少的库或环境变量，而不是只返回模糊连接失败。

验收门：使用驱动 mock 或可控连接测试覆盖 DSN、敏感信息、超时和错误分类；真实连接验收单独记录，不能由 sqlmock 代替。

### [ ] 阶段 3：实现 DB2 元数据读取与类型映射

- 从 DB2 catalog 读取 schema、table、column、primary key、unique index 和普通 index；将 catalog SQL 集中在 `db2util`。
- 新增 DB2 元数据结构，并在边界层映射为现有 `model.TableInfo`，避免让 catalog 字段渗透到整个 diff 流程。
- 建立显式类型映射表并写测试，至少覆盖：SMALLINT/INTEGER/BIGINT、DECIMAL/NUMERIC、REAL/DOUBLE、CHAR/VARCHAR、GRAPHIC/VARGRAPHIC、DATE/TIME/TIMESTAMP、BOOLEAN、BINARY/VARBINARY、BLOB/CLOB/DBCLOB。
- 对 DECFLOAT、XML、ROWID、特殊时间类型和超大 LOB 设定明确策略：支持、降级为字符串/字节，或在 V1 拒绝；不得静默转换。
- 检查源/目标列数量、顺序、主键/唯一键和类型兼容性，输出逐列诊断。

验收门：catalog SQL 有 golden/sqlmock 测试；类型映射表每个条目都有测试；不支持类型在真正扫描数据前失败并指出 schema.table.column。

### [ ] 阶段 4：把共享的 MySQL SQL 字符串改为结构化范围与方言渲染

- 审查 `sync_diff_inspector/chunk/`、`splitter/` 和 `RangeInfo`，避免把一段 MySQL `WHERE` 字符串直接交给 DB2。
- 保留列、上下界、开闭区间、NULL 语义、参数值和排序键等结构化信息，由每个方言分别渲染 SQL。
- 定义最小 `Dialect` 能力：引用标识符、限定表名、占位符、范围谓词、NULL 排序、分页/取中点、ORDER BY、类型转换。
- MySQL/TiDB 方言输出必须与现有语义一致；DB2 方言使用 DB2 合法的标识符、分页和表达式。
- checkpoint 只保存数据库无关的结构化范围。如果序列化结构变化，应增加版本或明确拒绝旧 checkpoint。

验收门：同一个结构化范围分别生成 MySQL/TiDB 与 DB2 golden SQL；覆盖复合键、字符串、NULL、二进制、时间、上下界相等和恢复 checkpoint。

### [ ] 阶段 5：实现适合 DB2 的 V1 分块策略

- 新增 `DB2TableAnalyzer` 和 `DB2Source`，接入 `buildSourceFromCfg`。
- V1 优先使用用户配置的 `fields`，否则选择 primary key，再选择可用的 non-null unique index。
- V1 不读取 TiDB/MySQL bucket histogram；优先复用 limit/random 思路，但 SQL 必须由 DB2 方言生成。
- 没有稳定唯一排序键时，不得假设 OFFSET 分页可靠：要求用户配置字段，或降级为单块并明确性能警告。
- 对复合键使用稳定的字典序边界；验证 DB2 与 TiDB 的 NULL 顺序、字符排序和大小写差异。

验收门：分块覆盖全部行、块之间不重叠且顺序稳定；测试空表、单行、重复值、复合键、NULL、非 ASCII 字符和断点恢复。

### [ ] 阶段 6：定义跨数据库 CanonicalV1 行编码和摘要协议

- 不复用 `GetCountAndMd5Checksum` 的 MySQL 专用 SQL 作为 DB2 摘要，因为不同数据库的字符串转换、NULL、浮点、时间和编码规则无法保证相同。
- 增加带版本号的 `CanonicalV1` 编码：固定列顺序，每列包含 NULL 标志、类型标志、长度前缀和规范化字节；禁止简单用逗号拼接。
- 明确规范化规则：DECIMAL 保留精度与 scale；浮点处理 NaN/Inf/负零和比较容差；时间统一时区与精度；CHAR 尾空格策略；字符串统一编码但不擅自改变语义；二进制保持原字节；LOB 采用流式读取或明确大小上限。
- DB2 → TiDB 模式下，两端都通过相同的 Go 侧 CanonicalV1 计算每块 count 和 digest；digest 不同再进入逐行差异定位。
- 摘要结果必须携带算法版本，禁止比较不同算法得到的数值。

验收门：同一逻辑值从 DB2 驱动值和 TiDB 驱动值进入规范化后得到相同字节；NULL 与空串、整数与小数、时区、尾空格、Unicode、二进制和浮点边界都有对照测试。

### [ ] 阶段 7：实现 DB2 行迭代、排序与差异定位

- DB2 查询使用结构化范围和稳定 ORDER BY，扫描结果转换为通用的 canonical row，而不是依赖 MySQL 驱动返回的字节格式。
- 重构 `RowDataIterator`/`ColumnData` 边界，使比较层能区分数据库原始值与规范化值。
- 排序比较使用类型感知规则；不能继续把所有非字符串值转成 `float64`，否则 BIGINT、DECIMAL 和高精度时间会丢精度。
- 保证源 DB2 行与目标 TiDB 行按同一个逻辑键归并；发现重复键时给出确定性行为和诊断。
- `GenerateFixSQL` 属于下游目标能力。将它从通用 Source 职责中拆分，或至少确保只调用 TiDB target 的实现；DB2Source 不生成 DB2 修复 SQL。

验收门：构造相同、仅源有、仅目标有、字段不同、重复键和大数值样例，逐行差异计数及导出的 TiDB 修复 SQL均正确。

### [ ] 阶段 8：配置、日志、错误和兼容性收口

- 增加一个最小 DB2 → TiDB 配置示例，解释 database/schema、大小写、fields、连接池、隔离要求和不支持类型。
- 在启动阶段输出非敏感能力摘要：源/目标类型、比较表数、分块键、CanonicalV1、并发数和一致性模式。
- 为所有 DB2 SQL 错误增加表、阶段和操作上下文，但不打印密码、证书内容或完整敏感 DSN。
- 保留原 MySQL/TiDB 配置格式和行为；不得要求现有用户新增 `type` 才能运行。
- 更新根 README 和 `sync_diff_inspector/README.md`，明确离线测试与真实 DB2 验收的区别。

验收门：旧配置测试通过；新示例能被解析；错误消息能够定位到具体阶段和对象；日志无凭据泄漏。

### [ ] 阶段 9：分层验证并提交证据

- 静态层：运行格式化、`go vet`（适用包）和 `git diff --check`。
- 单元层：运行 `go test ./sync_diff_inspector/...` 以及新增 DB2 包测试；测试必须包含方言、catalog、类型、canonical、范围和错误路径。
- 回归层：运行现有 MySQL/TiDB Source、splitter、checkpoint、row iterator 和 fix SQL 测试。
- 真实数据库层：在明确 Db2 LUW 版本与 TiDB 版本上完成最小端到端用例。
- 性能层：记录至少一个中等数据量样例的行数、耗时、峰值内存、连接数和 chunk 数；V1 可以慢，但不能无限制把整表加载进内存。

真实端到端数据集至少包含：

- 空表、单行表、无差异表。
- 源多行、目标多行、同键不同值。
- 复合主键、无主键但有唯一键。
- NULL、空串、CHAR 尾空格、Unicode/中文。
- BIGINT 边界、DECIMAL 高精度、浮点边界。
- DATE/TIME/TIMESTAMP 和时区差异。
- BINARY/VARBINARY，以及在 V1 策略允许范围内的 LOB。
- quoted identifier、DB2 默认大写 schema/table、保留字列名。
- checkpoint 中断和恢复。

验收门：分别列出“单元测试通过”“真实 DB2 连接通过”“真实端到端通过”；缺少真实数据库时只能标记前两层中的实际完成项，不能声称 DB2 功能完成。

### [ ] 阶段 10：提交与交付

- 建议按以下边界提交：配置/连接、元数据、方言/结构化范围、分块、CanonicalV1、行迭代/修复 SQL、文档/测试。
- 最终汇报分支名、commit SHA、驱动与原生依赖、支持的 Db2 版本、类型矩阵、配置示例、测试命令和每层验证结果。
- 单独列出未验证项、已知性能限制、实时写入一致性限制以及下一阶段优化建议。
- 不要在用户未要求时合并回 `feature/sync-diff-standalone` 或 `master`。

验收门：工作树干净，提交历史可审阅，默认分支未改变，报告中的每项“通过”都有命令输出或真实数据库证据。

## 6. 预计新增或重点修改的文件

- `sync_diff_inspector/config/config.go`
- `sync_diff_inspector/config/template.go`
- `sync_diff_inspector/source/source.go`
- `sync_diff_inspector/source/db2.go`（新增）
- `sync_diff_inspector/source/db2_test.go`（新增）
- `sync_diff_inspector/db2util/`（新增，连接、catalog、类型和方言）
- `sync_diff_inspector/source/common/table_diff.go`
- `sync_diff_inspector/source/common/rows.go`
- `sync_diff_inspector/chunk/`
- `sync_diff_inspector/splitter/`
- `sync_diff_inspector/checkpoints/`
- `sync_diff_inspector/utils/`（只保留真正通用部分；MySQL SQL 继续隔离）
- `go.mod`、`go.sum`
- `README.md`
- `sync_diff_inspector/README.md`
- `sync_diff_inspector/config/config_db2.toml`（新增示例）

## 7. 关键风险

| 风险 | 要求 |
|---|---|
| DB2 与 TiDB 服务端 checksum 不同 | V1 两端统一使用 CanonicalV1，不比较不同算法。 |
| BIGINT/DECIMAL 被转为 float64 | 类型感知比较，禁止用 float64 作为通用数值中间表示。 |
| DB2 标识符默认转大写 | catalog、路由、引用与展示必须分清规范名和原始名。 |
| 共享 MySQL WHERE SQL | 保存结构化范围，由各自 Dialect 渲染。 |
| 无唯一排序键 | 要求 fields、选择唯一键，或单块降级并警告。 |
| 多连接没有共同快照 | V1 明确静止数据要求或一致性限制，不做虚假强一致承诺。 |
| LOB 导致内存暴涨 | 流式处理或设置明确上限，不能整表/整列无界缓存。 |
| DB2 驱动依赖原生库 | 构建和运行前检查，并在文档中列出可复现安装要求。 |
| Source 接口含 target 职责 | 拆分读取能力与修复 SQL 生成能力，避免 DB2Source 实现无意义方法。 |
| 只有 sqlmock 证据 | 把 mock、真实连接和真实端到端证据分层报告。 |

## 8. 可直接交给另一个模型的提示词

下面整段可以原样复制：

---

你现在负责在本地仓库 `D:\codex-WorkSpace\sync-diff-standalone` 中实现 Db2 LUW → TiDB 数据对比。先完整阅读仓库根目录的 `DB2_SOURCE_IMPLEMENTATION_HANDOFF.md`，并严格按阶段和验收门执行。

基线要求：

1. 从 `feature/sync-diff-standalone` 创建 `feature/db2-source`，不要直接修改或合并基线分支、master。
2. 当前计划基线 SHA 是 `79f010dcd074dd4fdf380804b57ad7f9c8feb466`；开始前核对实际 SHA，如有漂移先分析差异并更新计划，不要盲目覆盖。
3. 本次只实现 Db2 LUW 作为上游、TiDB 作为下游。不要实现 DB2 target、CDC、分片 DB2、自动执行修复 SQL或 DB2 服务端 checksum 优化。
4. 保持现有 MySQL/TiDB 配置和行为兼容。旧配置未填写数据库 type 时仍应按原逻辑工作。
5. 正确性优先：DB2 → TiDB 的 V1 必须让两端使用相同的 Go 侧 CanonicalV1 行编码和 chunk digest。不要直接拿 DB2 自己的 hash SQL 与现有 MySQL MD5 SQL结果比较。
6. 不要把 MySQL WHERE、反引号、分页、元数据 SQL直接复用到 DB2。范围必须保留结构化语义，并由 MySQL/TiDB 和 DB2 Dialect 分别渲染。
7. 不要把 BIGINT、DECIMAL、时间或所有非字符串值统一转换为 float64。建立类型感知的规范化、排序和比较规则。
8. DB2 驱动、Db2 LUW 支持版本、原生库依赖和一致性假设必须写清楚。若缺少真实 DB2/TiDB 环境，完成离线代码和测试后明确标注真实连接与端到端验收未完成，不能宣称功能完成。
9. 先读代码和测试，再改动。小步提交，每阶段完成后运行相关测试并记录结果；保留用户现有修改，不使用 git reset --hard 等破坏性命令。
10. 最终提交完整报告：分支、commit SHA、修改文件、驱动依赖、类型矩阵、配置示例、测试命令/结果、真实 DB2 证据、未验证项和后续优化。

实施顺序严格按交接文档的阶段 0–10：基线 → 配置/连接 → 元数据/类型 → 结构化范围/Dialect → 分块 → CanonicalV1 → 行迭代/差异定位 → 文档/兼容性 → 分层验证 → 交付。遇到必须由用户决定的驱动、版本或一致性问题时，先完成不依赖该选择的只读分析，并用一个简短问题向用户确认，不要擅自扩大范围。

---
