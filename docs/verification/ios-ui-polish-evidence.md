# iOS UI 优化验证记录

2026-10-18。设计约定见 [ios-ui-polish-design.md](ios-ui-polish-design.md)。实际 SwiftUI 界面已修改并同步回项目，后端、回复队列和认证策略没有改变。未新增依赖或初始化 Git。

本轮依据 Apple 的 [Alerts](https://developer.apple.com/design/human-interface-guidelines/alerts)、[Sheets](https://developer.apple.com/design/human-interface-guidelines/sheets) 和 [Accessibility](https://developer.apple.com/design/human-interface-guidelines/accessibility) 指南：关键后果用简短系统确认框，编辑使用 Sheet，保留取消操作，支持系统字号和足够大的点击区域。

## 最终结果

| 配置 | 单元测试 | UI 测试 | 失败 / 跳过 | 实际截图 |
| --- | --- | --- | --- | --- |
| iPhone 17 Pro，iOS 26.5，浅色，系统 large 字号 | 23 | 13 | 0 / 0 | 22 |
| iPhone SE 3，iOS 26.5，深色，accessibility-large 字号 | 23 | 13 | 0 / 0 | 22 |
| 同步回原项目后的编译与单元复验 | 23 | 未重复完整 UI 套件；源码哈希一致 | 0 / 0 | — |

两套测试使用相同最终源码，前后哈希一致。Go race 套件也通过（后端未改，使用 Go 缓存结果）。Xcode 编译完成静态类型检查。详细设备、版本、结果、覆盖率和原项目复验见 [ios-ui-polish-results.json](ios-ui-polish-results.json)。

最终源码 SHA-256：`3b3cc67f01b7ac16944168748f1c4b20df7aee03481682612225a4066bae4dcc`。哈希算法位于验证脚本，覆盖 Swift、Go、Python、项目配置和字符串目录。开发在临时完整副本中进行，确认原目录未发生并行代码修改后，只同步变动文件；未覆盖其他文件。

## 已核对的行为

- 服务器切换、放弃回复和创建新密钥使用系统 Alert，检查系统控件类型、水平中心位置、屏幕中央区域及完整边界；中英文均检查。标准“取消”保留原身份与已保存回复。
- 服务器编辑 Sheet 支持中等及完整高度；大字体使用完整高度。地址无效时先收起键盘，错误在输入框下可见，不额外弹错误框。
- 筛选项实际点击范围至少 44pt；空列表不会挡住筛选。选中状态同时提供对勾和底色，待处理问题有文字状态。
- 回复输入栏固定在键盘上方，正文独立滚动，输入框最多显示三行并可内部滚动。发送按钮布局为 48pt，自动化检查其实际点击范围不小于 44pt；输入多行文字后仍可点击，边界位于键盘上方。完成操作收起键盘但保留完整草稿。
- 引导内容可滚动，主要按钮在安全区域内；详情页收起底部 Tab，返回恢复；离线状态明确显示“等待发送”。
- 真正调用临时 Go 服务验证文本回复、拒绝审批、自定义选项；HTTP 503 后保留回复，取消切换后重启能补发；明确放弃后检查实际队列文件为空，原服务器问题仍未回答，重启保持新服务器注册页。

关键 UI 测试为 `DesignRegressionUITests` 的六项检查，以及原有七项流程。既有 23 项单元回归保持不变。

## 截图

全部来自实际 XCTest 截图附件，不是设计效果图；只导出明确命名的图片，未导出展示完整 Device Key 的自动录屏。

| 画面 | 标准浅色 | 小屏、大字体、深色 |
| --- | --- | --- |
| 中文切换确认 | [截图](ios-ui-polish-screenshots/wide/server-switch-chinese.png) | [截图](ios-ui-polish-screenshots/compact/server-switch-chinese.png) |
| 有待发送回复的确认 | [截图](ios-ui-polish-screenshots/wide/server-switch-choices.png) | [截图](ios-ui-polish-screenshots/compact/server-switch-choices.png) |
| 键盘与多行回复 | [截图](ios-ui-polish-screenshots/wide/reply-keyboard.png) | [截图](ios-ui-polish-screenshots/compact/reply-keyboard.png) |
| 服务器编辑 | [截图](ios-ui-polish-screenshots/wide/server-editor.png) | [截图](ios-ui-polish-screenshots/compact/server-editor.png) |
| 无效地址提示 | [截图](ios-ui-polish-screenshots/wide/server-invalid-address.png) | [截图](ios-ui-polish-screenshots/compact/server-invalid-address.png) |
| 审批问题 | [截图](ios-ui-polish-screenshots/wide/approval-detail.png) | [截图](ios-ui-polish-screenshots/compact/approval-detail.png) |
| 初次引导 | [截图](ios-ui-polish-screenshots/wide/onboarding-welcome.png) | [截图](ios-ui-polish-screenshots/compact/onboarding-welcome.png) |

确认框、服务器编辑、列表、详情、输入键盘和引导截图均作过视觉核对；小屏截图保留系统大字号，长内容通过滚动阅读。

## 迭代中发现并修复

[初始回归](ios-ui-polish-red-results.json) 的两项测试均失败：创建密钥和切换服务器都没有使用系统 Alert。后续检查发现发送图标虽然留出了布局空间，实际点击区域仍只有 36pt，已用明确点击形状修复，保留不小于 44pt 的断言。

大字体下，原有测试假定密钥按钮始终位于首屏，导致失败；改为滚动找到按钮并要求实际可点击，未移除原断言目标。随后视觉核对发现 [旧回复区](ios-ui-polish-screenshots/before/keyboard-covered-send.png) 会被键盘遮住，之前的几何检查只覆盖输入前，未检出这一情况；增加输入后按钮可点击及位于键盘上方的断言，并把输入栏移出正文滚动区。收尾时将输入栏改为实色背景，把“完成”放入栏内，避免大字体正文透到操作区。最终两套完整流程均重新通过。

## 复现

使用现有 Xcode 26.6（17F113）、Go 1.27.2 和 Python 标准库。验证脚本只允许名称包含 `Meow Fix` 的专用模拟器，会重置该模拟器的 App 与 Keychain；不会重置日常模拟器。每次输出目录必须不存在。

```sh
xcrun simctl ui <标准模拟器 UDID> appearance light
xcrun simctl ui <标准模拟器 UDID> content_size large
python3 ios/scripts/verify_review_fixes.py --simulator-id <标准模拟器 UDID> --output /tmp/meow-ui-wide-new

xcrun simctl ui <小屏模拟器 UDID> appearance dark
xcrun simctl ui <小屏模拟器 UDID> content_size accessibility-large
python3 ios/scripts/verify_review_fixes.py --simulator-id <小屏模拟器 UDID> --output /tmp/meow-ui-compact-new
```

结果目录包含 xcresult、日志、22 张命名截图和 summary.json。脚本拒绝测试失败、跳过、缺失截图或验证期间源码变化。本轮未重跑旧任务的可选 mutation 测试，也没有声称覆盖所有 HIG 条目或所有字号。未做完整 VoiceOver/色彩对比度审计；硬件生物认证、真实 APNs、iOS 17 运行时及横屏未验证，应用仍仅支持竖屏 iPhone。
