# Task4a Fix Round 1

基线 `92f470c`，分支 `codex/portable-v0.0.1`。工作目录 `C:\Users\cy\.codex\worktrees\portable-v0-0-1\Hack3rX_2026_011_ARTEX`。先完整读取 `task-4a-review.md`，按 receiving-code-review、TDD 和 verification-before-completion 核验并统一修复 I1–I4；Minor 六处重复 `d.SQL(d.SQL(...))` 已压为单层。没有访问 `G:\ARTEX-0.3.15`，没有调用模型/MCP/通知，没有修改原 PG 身份、网络 family、公司 scope normalization 或存储排序。

## 核验、RED 与实现

通过本次受控容器只读 psql oracle 核验原审查：

```text
0.9e2::jsonb::text        90
0.09e3::jsonb::text       90
-0::jsonb::text           0
-0.0::jsonb::text         0.0
1.20e-2::jsonb::text      0.0120
mapped IPv6 <<= v4 cidr   false
family(mapped IPv6)       6
name:1 ->>name            1
name:"1" ->>name          1
name:null ->>name         SQL NULL
```

Oracle 使用 `docker exec vulnharvest-portable-test-pg psql -U vh_test -d vh_assets -At -v ON_ERROR_STOP=1 -c ...`，只执行 SELECT。又确认 `cidr '::ffff:192.0.2.0/24'` 会报 `Value has bits set to right of mask`：原 `calcCSegment(mapped)` 另有此前置限制，因此这里**不声称原 mapped UpsertIP 全流程已可成功**。按协调者指示，不扩大修复这个既有 helper，mapped 回归通过合法直接 seed 的资产/网络验证 resolve/recompute/DSL。

新增双库反例组的原实现 RED 命令：

```powershell
$env:ARTEX_ASSET_TEST_PG_DSN='postgres://vh_test:vh-portable-test-only@127.0.0.1:27347/vh_assets?sslmode=disable'
go test ./db -run '^TestPortableAssetReviewFixContract$' -count=1 -timeout=120s -json > '.superpowers/sdd/portable-v0.0.1/task-4a-fix-1-red.jsonl'
```

原实现的决定性反例：SQLite `0.9e2→090`、`0.09e3→0090`、`-0→-0`、`-0.0→-0.0`，params 90/8 错选8；number/string同名 params 返回2项；mapped Resolve 匹配了v4公司，v4 declared-scope 返回mapped行；asset UTC裸字符串无法按带时区API格式解析。PG 对照通过（PG子组5.44s）。两个测试 fixture 预期先按真实行为校正：公司两库 touch trigger 均把 updated_at 设为 now，应与 typed实际instant比较；mixed location 的全对象 C DESC先比较 value，实际winner是string，不是numeric。没有为通过测试修改 PG 行为。

- **I1**：SQLite资产 `last_seen`、公司 `created_at/updated_at` 输出 `replace(CAST(... AS TEXT),' ','T') || 'Z'`，保留9位fraction。底层UTC存储和排序不改，PG原 `::text` 不改。测试把 +05:30 输入通过真实bind写入，再从 Asset/Company Get/GetByName/List API字符串JSON往返，按显式时区解析并比较同一瞬间；updated_at和真实trigger输出的typed时间对照。临时恢复原CAST（finally立即还原）后，用正确fixture再次证明两个SQLite时间测试都RED：`2026-10-01 02:45:03.123456000` 无时区，无法解析；日志 `task-4a-fix-1-i1-red.log`。当前输出是 `2026-10-01T02:45:03.123456000Z`。
- **I2**：`pgJSONNumberText` 使用精确十进制digits/point/scale，去整数前导零、零值去负号，保留fractional scale。没有float64，也没有用RatString替代排序文本。覆盖0.9e2、0.09e3、-0、-0.0、-0.0e-2、1.20e-2、1.20e2、大整数带scale、长精确小数；真实endpoint merge验证90胜8、0.0胜0、scale与大数选择正确。数字text+merge双库GREEN 2.920s。
- **I3**：`pgJSONTextKey`/`pgParamGroupKey` 按PG `->>` nullable text生成二元key：string去引号，其他非null类型用精确jsonb canonical text，null/missing均SQL NULL；不引入string-only新校验，不改变PG身份SQL。覆盖number/string、指数/scale/负零key、bool/string、array/string、object/string、null/missing合组与null/"null"区分，以及location维度；保持原full-object C DESC winner。时间+初次分组反例合组双库GREEN 4.029s。
- **I4**：公司rules和SQLite网络UDF均改用 `net/netip`，同family（BitLen一致）才匹配，保留mapped IPv6为128 bit，**不调用Unmap**。Resolve/Recompute包括v4、原生v6、mapped v6；v4网络不归属mapped，真实family6 mapped-prefix可以归属；DSL count/query/GetByIDsInScope验证mapped IP列与IP-literal domain两条路径、v4范围不泄漏mapped、family6范围排除v4。双库GREEN 2.913s。

完整新反例组（补齐指数/负零/scale mixed-key后）GREEN：同一focused命令，exit0，`ok github.com/Autumn-27/artex/db 7.515s`；SQLite子组0.65s，PG子组5.58s，0 fail/skip。见 `task-4a-fix-1-green.jsonl`。

## 最终验证与自审

原contract受共有numeric/network/time接缝影响，因此所有修复完成后必要地执行一次原契约+新反例统一suite：

```powershell
go test ./db -run '^(TestPortableAssetContract|TestPortableAssetReviewFixContract|TestParseScopeLine|TestParseAutoScopeLine|TestParseStructuredCompanyScope|TestCalcCSegment|TestMarshalStringArray|TestNormalizeURL|TestCompanyScopeMutationLockDoesNotReuseInfrastructureLocks)$' -count=1 -timeout=180s -json > '.superpowers/sdd/portable-v0.0.1/task-4a-fix-1-final-suite.jsonl'
go vet ./db
git diff --check
```

最终suite exit0：`ok github.com/Autumn-27/artex/db 117.942s`。**179 test pass事件**（含子组），另有package pass；原SQLite55 case、原PG54 case；新增7个反例组/每库，含9项NumericText子测试后各16个case/subcase pass；**0 fail、0 skip**。原SQLite组5.30s、原PG99.19s；新SQLite1.35s、新PG10.65s。`go vet ./db` 与 `git diff --check` 在完整代码状态下再次exit0，无输出；未用旧PASS代替最终gate。提交前只对指定文件stage，staged diffcheck也须exit0。

自审：I1修改仅SQLite SELECT显示表达式，PG SQL/UTC driver不变；I2保留scale而不破坏auth的独立数值等价key；I3 nullable pair编码无类型/分隔符冲突，空/missing和literal字符串不混淆；I4所有Contains路径均用netip family，不改共享scope parsing或asset自然键；transaction/reader接口不变。Minor清理只去掉6处冗余方言层。原报告尾已补此修复交接，本报告单独保留，历史初轮结果不改写。

I1–I4无已知未解决项。边界保持原任务范围：不新增超出PG numeric可表示范围的统一校验；原calcCSegment mapped前置限制已如实记录，按指令没有扩大修复；Task3/contextCoverage、finding/archive/runtime、全项目性能/race仍属后续gate。测试factory继续仅guard/reset专用 `vh_assets`，SQLite全部TempDir/Fatal不Skip；日志保留工作树不stage，只提交本轮代码、测试、两份报告，排除 `implementation/verification.md`。

提交信息：`fix(db): preserve asset time numeric and network semantics`。提交SHA由交接回复给出，避免报告自引用。测试原始记录：`task-4a-fix-1-red.jsonl`、`task-4a-fix-1-i1-red.log`、`task-4a-fix-1-green.jsonl`、`task-4a-fix-1-final-suite.jsonl`，均保留此工作树。
