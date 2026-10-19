# TestFlight 0.1.0 (8) — 2026-10-20

复审结论：上次报告的三项 UI 问题已修复，本次范围内未发现新的发布阻塞问题。

- 待处理问题通过现有 pending API 遍历全部分页，再按到期时间排序；普通通知仍保持分页。后续分页失败会报告失败，不把不完整集合当成完整结果。
- 待处理列表仅显示仍可回答的问题；回答或过期后移除，并更新本地待处理计数。
- CLI 安装帮助改为需要仓库权限的源码构建流程，去掉不存在的 Release 下载入口。

## 本次独立验证

- 新执行原生 macOS XCTest 适配测试：91 项通过，0 失败；覆盖实际 AppModel、API、离线回复和通知处理代码。边界存储和通知系统使用适配器，不能替代 iPhone UI/推送测试。
- 核对已有 UI 测试记录的 11 项源码 SHA256：全部匹配；查看了跨页置顶及安装帮助截图。本次没有重新启动模拟器。既有 iOS 记录为 91 项单元测试 + 7 项 UI 流程通过；最终待处理数量调整后，另有 1 项回答/过期 UI 流程通过。见 [原记录](ui-review-fixes-results.json)。
- 本次 iPhone Release archive 成功。归档、导出 IPA 均通过 verify_distribution.py；主 App 和通知扩展均为 0.1.0 (8)，正式包使用 production APNs、App Store provisioning、不可调试签名；App Group、时效性通知、隐私清单均通过检查。
- 两个 target 均包含原 meow.wav；SHA256 为 995a156c4cf66ac2ad0766c894a147f6032ea8efa6bf20668a38310615911773。
- 打包期间记录的 44 项 iOS 源码/资源哈希未变化；本次唯一产品配置修改是 build 从 7 增至 8。git diff --check 与本地化 JSON 校验通过。
- Xcode 输出一条无 AppIntents.framework 依赖、跳过元数据提取的提示；归档成功。

## 上传结果

2026-10-20 04:05:49 +08:00，xcodebuild 返回 Upload succeeded / EXPORT SUCCEEDED，退出码 0。Apple 回报 Uploaded package is processing。未据此推断已处理完成、已自动分发给测试组或已在手机安装。

本次尚未对 build 8 做真机 UI/APNs 验证。此前公网蜂窝测试属于之前安装的版本，记录没有复用为 build 8 的通过证据。

本地制品及日志位于 dist/testflight/0.1.0-8/：Meow.xcarchive、export/Meow.ipa、review-native/results.json、archive-inspection.json、ipa-inspection.json、source-files.json、status.json、archive.log、export.log、upload.log。未修改用户 CLI 凭据或运行中的服务器。
