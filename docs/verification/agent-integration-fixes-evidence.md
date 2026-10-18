# Agent 集成修复验证记录

2026-10-18。范围见 [修复约定](agent-integration-fixes-spec.md)。四项问题均已修复；没有修改实际安装的 Agent 配置，没有提交 Git。

## 最终结果

所有数字来自同步回原目录后的最终复验：

- `go test -race -shuffle=20261018 -count=1 -coverpkg=./... -json ./...`：112 项通过（含子用例和模糊测试种子），0 跳过；覆盖率参数和实际命令见脚本。
- 本轮修改的 Go 实现：58 个可执行变更行被测试覆盖，0 个未覆盖。Go 提供语句块计数，不等于完整分支覆盖。
- 覆盖率门禁的零计数负对照被拒绝。
- `go build ./...`、`go vet ./...`、CLI 文件的 gofmt 检查均通过。
- 5 项手工故障注入全部被实际运行的测试检出：错误归属、跳过续跑、提示级幂等键、忽略 Skill 路径、忽略带引号键。最后一项单独运行 TOML 性质测试的种子用例。
- 200 个固定随机种子的 TOML 更新／卸载往返性质检查通过；5 秒原生 Go fuzz 通过，最终运行执行 186734 次。
- 真实编译的 CLI 对临时 HTTP 服务发送三次 Hook 事件，产生两个不同通知和一次重复投递；同提示下续跑的新摘要不再与旧请求冲突。
- 真实 CLI 在临时 Claude 配置目录完成安装、重复安装和卸载；特殊空格、单引号路径下生成的 Skill 命令可执行。
- `govulncheck v1.9.0`：No vulnerabilities found。新运行时依赖只有 `github.com/pelletier/go-toml/v2 v2.4.3`，MIT 许可，许可全文见 [文件](go-toml-v2-LICENSE.txt)。SQLite 仍固定 v1.60.1。
- 项目 `bin/meow` 已重新编译。

## 行为与用例

| 行为 | 回归用例 |
| --- | --- |
| 带引号、转义键的 notify 冲突与显式替换 | TestCodexQuotedNotifyConflict |
| 注释、转义三引号、多行文本、CRLF、表头下保持根级配置 | TestCodexParserKeepsNotifyAtRoot |
| 无效 TOML 在写入前被拒绝 | TestCodexRejectsInvalidDocuments |
| 其他程序带 hook/codex 参数也不能被覆盖或卸载 | TestCodexForeignHookArgumentsArePreserved |
| 自定义名称的当前 Meow 程序可重复安装和卸载 | TestCodexCustomExecutableOwnership / TestCodexPlanKeepsCustomExecutableAndForeignCommands |
| 续跑完成的通知不丢失，不同摘要不共用幂等键 | TestBuildClaudeStop / TestClaudeCompletionIdempotencyAfterContinuation |
| Skill 使用安装路径，支持空格与单引号 | TestSkillCommandsUseInstalledExecutable |
| 无设备密钥也能卸载，保持原流程 | TestSetupUninstallNeedsNoDeviceKey |
| 非相关设置保持不变、重复安装稳定 | TestCodexGeneratedRoundTrips / FuzzCodexConfigPreservesSettings |

## 复现

```sh
python3 scripts/verify_agent_fixes.py --output /tmp/meow-agent-fixes-new-run
```

输出目录必须不存在。脚本依次运行测试、覆盖率门禁及负对照、构建、vet、格式检查、fuzz、故障注入、实际 CLI、漏洞检查；任何步骤失败即退出且不生成成功结果。变异在临时副本中执行，并要求失败来自已执行的测试，而非编译失败。它使用临时 CLI/Claude 配置和模拟 HTTP 服务，不给手机发通知。

Go：go version go1.27.2 darwin/arm64。漏洞工具固定 v1.9.0。源码状态 SHA-256：`b6075d1de99b9baa77faf22e41c128a890dd70078c029f112894d309acc709a2`，覆盖全部 Go 源码和测试、go.mod/go.sum、验证脚本，脚本会拒绝验证期间源码变化。结构化结果见 [JSON](agent-integration-fixes-results.json)。原工作区已有的 146 个非本轮文件在同步时通过哈希核对。

## 过程与边界

- RED 阶段逐项观察到失败，原始输出保存在 [日志](agent-fixes-red.log)。既有 Stop 测试的期望随本次已确认的行为修正；没有删除断言或放宽失败条件。
- 初次接入 AST 时错误使用了表节点的空 Raw 范围，既有格式测试立即失败；已改用键节点的源位置。未更改原有格式断言。
- 覆盖率工具最初没有统计跨包执行，随后还需要合并多个测试二进制对同一块的计数；已修正并运行零计数负对照。门禁是自编检查器，这一对照只证明其拒绝零计数输入，不能证明识别所有可能的覆盖率异常。
- 使用临时 Go 项目副本隔离实现，包含当时已有的服务端改动；只同步本轮 CLI、测试、依赖和验证文件。原项目的部署、iOS 和服务端文件没有被覆盖。原目录再次完成完整验证。
- 本次按用户已授权的修复范围自主执行；spec approval: not obtained (autonomous run)。独立代理验证未执行；staticcheck 不可用，使用 go vet。没有声称通过独立审计。
- 本轮未重复真实 Agent 会话、iOS 模拟器、Face ID 或 APNs 验证。Stop 通知只表示本次响应结束，不能替其他 Stop Hook 保证整个任务已获准停止。模糊测试的保留性质针对 TOML 语法及合法的 notify 字符串数组形状，不是 Codex 全配置 Schema 验证。
