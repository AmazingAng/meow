import XCTest

/// Run with verify_ui_optimization.py against its disposable fixture and simulator.
final class UIOptimizationTests: XCTestCase {
    private var server: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let url = ProcessInfo.processInfo.environment["MEOW_UI_OPTIMIZATION_SERVER"] else {
            throw XCTSkip("run ios/scripts/verify_ui_optimization.py")
        }
        server = url
        let done = expectation(description: "fixture reset")
        URLSession.shared.dataTask(with: URL(string: url + "/fixture/reset")!) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 5)
    }

    private func open(id: String? = nil, large: Bool = false, chinese: Bool = false) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-meowServer", server, "-meowAutoRegister", "YES",
                               "-AppleLanguages", chinese ? "(zh-Hans)" : "(en)",
                               "-AppleLocale", chinese ? "zh_CN" : "en_US",
                               "-UIPreferredContentSizeCategoryName", large ? "UICTContentSizeCategoryAccessibilityXXXL" : "UICTContentSizeCategoryL"]
        if let id { app.launchArguments += ["-meowOpen", id] }
        app.launch()
        return app
    }

    private func capture(_ name: String, _ app: XCUIApplication) {
        let shot = XCTAttachment(screenshot: app.screenshot()); shot.name = name; shot.lifetime = .keepAlways; add(shot)
    }

    private func reveal(_ element: XCUIElement, in app: XCUIApplication) {
        // SwiftUI can report a clipped ScrollView child as hittable behind its footer.
        let bottom = app.tabBars.firstMatch.exists ? app.tabBars.firstMatch.frame.minY - 8 : app.frame.maxY - 140
        let visibleArea = CGRect(x: app.frame.minX, y: app.frame.minY + 100,
                                 width: app.frame.width, height: bottom - app.frame.minY - 100)
        for _ in 0..<30 {
            var moveUp = true
            if element.exists {
                let center = CGPoint(x: element.frame.midX, y: element.frame.midY)
                if element.isHittable && visibleArea.contains(center) { break }
                moveUp = center.y > visibleArea.midY
            }
            let start = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: moveUp ? 0.65 : 0.4))
            let end = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: moveUp ? 0.4 : 0.65))
            start.press(forDuration: 0.1, thenDragTo: end)
        }
        XCTAssertTrue(element.isHittable)
    }

    func test31LocalUnreadBadgeDoesNotDependOnServerReads() {
        scenario("grouping")
        let app = open()
        browse("sessions", in: app)
        let row = app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@ AND label CONTAINS %@", "inbox.group.", "Deploy")).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        XCTAssertEqual(row.value as? String, "2 matching unread messages")
        row.tap()
        XCTAssertTrue(app.buttons["group.search"].waitForExistence(timeout: 5))
        app.navigationBars.buttons.element(boundBy: 0).tap()
        let read = expectation(for: NSPredicate(format: "value == %@", "0 matching unread messages"), evaluatedWith: row)
        wait(for: [read], timeout: 5)
        assertInboxStats(unread: 3, total: 5)
        capture("local-unread-authority", app)
    }

    func test01GuidedSetupAndBackNavigation() throws {
        scenario("onboarding-missing-key")
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()
        XCTAssertTrue(app.textFields["server.address"].waitForExistence(timeout: 10))
        XCTAssertEqual(app.textFields["server.address"].value as? String, "https://api.meow.blue")
        capture("setup-official-default", app)
        app.terminate()
        app.launchArguments = ["-meowServer", server, "-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()
        XCTAssertTrue(app.textFields["server.address"].waitForExistence(timeout: 10))
        capture("setup-welcome", app)
        let help = app.buttons["onboarding.serverHelp"]
        reveal(help, in: app); help.tap()
        XCTAssertTrue(app.navigationBars["Server address help"].waitForExistence(timeout: 5))
        capture("setup-server-help", app)
        app.buttons["Done"].tap()
        app.buttons["Get started"].tap()
        XCTAssertTrue(app.buttons["Not now"].waitForExistence(timeout: 10))
        app.buttons["Not now"].tap()
        let create = app.buttons["onboarding.createKey"]
        reveal(create, in: app)
        XCTAssertFalse(app.buttons["Next"].isEnabled)
        capture("setup-missing-key", app)
        create.tap()
        app.alerts.buttons["Create new key"].tap()
        XCTAssertTrue(app.alerts["Something went wrong"].waitForExistence(timeout: 10))
        app.alerts.buttons["OK"].tap()
        XCTAssertFalse(app.buttons["Next"].isEnabled)
        create.tap()
        app.alerts.buttons["Create new key"].tap()
        XCTAssertTrue(app.buttons["Copy Device Key"].waitForExistence(timeout: 10))
        XCTAssertNotEqual(app.staticTexts["onboarding.deviceKey"].label, "meow_recovered_ui_fixture")
        let maskedKey = app.staticTexts["onboarding.deviceKey"]
        reveal(maskedKey, in: app)
        maskedKey.press(forDuration: 1.2)
        XCTAssertFalse(app.menuItems["Copy"].exists)
        XCTAssertFalse(app.buttons["Copy"].exists, "masked text must not open the selection menu")
        reveal(app.buttons["Show Device Key"], in: app)
        app.buttons["Show Device Key"].tap()
        XCTAssertEqual(app.staticTexts["onboarding.deviceKey"].label, "meow_recovered_ui_fixture")
        app.buttons["Hide Device Key"].tap()
        XCTAssertNotEqual(app.staticTexts["onboarding.deviceKey"].label, "meow_recovered_ui_fixture")
        XCTAssertTrue(app.buttons["Next"].isEnabled)
        reveal(app.buttons["Share setup command"], in: app)
        capture("setup-share-and-masked-key", app)
        let cli = app.buttons["onboarding.cliHelp"]
        XCTAssertTrue(cli.waitForExistence(timeout: 5)); reveal(cli, in: app); cli.tap()
        XCTAssertTrue(app.navigationBars["Install the CLI"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["macOS · Homebrew"].exists)
        XCTAssertFalse(app.links["Download Meow CLI"].exists)
        capture("setup-cli-help", app)
        app.buttons["Done"].tap()
        app.buttons["Next"].tap()
        // The fixture already contains an agent notification, so onboarding
        // recognizes it and offers Done instead of waiting for another one.
        XCTAssertTrue(app.buttons["Done"].waitForExistence(timeout: 5))
        let enable = app.buttons["onboarding.enableNotifications"]
        reveal(enable, in: app)
        XCTAssertTrue(app.staticTexts["Notifications are off"].exists)
        capture("setup-test-notifications-off", app)
        app.buttons["onboarding.back"].tap()
        XCTAssertTrue(app.buttons["onboarding.cliHelp"].waitForExistence(timeout: 5))
        app.buttons["Next"].tap()
        XCTAssertTrue(app.buttons["Done"].waitForExistence(timeout: 5))
        app.buttons["Done"].tap()
        XCTAssertTrue(app.buttons["inbox.browseMenu"].waitForExistence(timeout: 10))
    }

    func testSettingsShareCommandAndCopyKey() {
        let app = open()
        XCTAssertTrue(app.buttons["inbox.browseMenu"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.tabBars.buttons["Settings"].exists)
        app.tabBars.buttons["Settings"].tap()
        let copyKey = app.buttons["settings.deviceKey"]
        reveal(copyKey, in: app)
        XCTAssertTrue(copyKey.label.contains("meo…ure") || (copyKey.value as? String)?.contains("meo…ure") == true)
        XCTAssertFalse(app.buttons["Copy Device Key"].exists)
        XCTAssertFalse(app.staticTexts["meow_disposable_ui_fixture"].exists)
        copyKey.tap()
        XCTAssertFalse(app.alerts.firstMatch.exists, "copy must not also trigger key rotation")
        // Paste into the unsaved server editor to verify the full fixture key,
        // rather than merely checking the copy confirmation or masked preview.
        assertCopiedText("meow_disposable_ui_fixture", in: app)
        let share = app.buttons["settings.shareSetup"]
        reveal(share, in: app); share.tap()
        let copy = app.descendants(matching: .any).matching(NSPredicate(format: "label IN %@", ["Copy", "拷贝", "复制"])).firstMatch
        XCTAssertTrue(copy.waitForExistence(timeout: 5))
        capture("settings-share-command", app)
        copy.tap()
        XCTAssertTrue(app.navigationBars["Settings"].exists)
    }

    private func assertCopiedText(_ expected: String, in app: XCUIApplication, chinese: Bool = false) {
        let server = app.buttons["settings.server"]
        reveal(server, in: app); server.tap()
        let field = app.textFields["server.address"]
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.tap()
        let current = field.value as? String ?? ""
        field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: current.count))
        field.press(forDuration: 1.1)
        let paste = app.descendants(matching: .any).matching(NSPredicate(format: "label IN %@", ["Paste", "粘贴"])).firstMatch
        XCTAssertTrue(paste.waitForExistence(timeout: 5)); paste.tap()
        XCTAssertEqual(field.value as? String, expected)
        app.buttons[chinese ? "取消" : "Cancel"].tap()
    }

    func test02InboxViewMenuOnlyOffersFourModes() {
        let app = open()
        XCTAssertTrue(app.buttons["inbox.browseMenu"].waitForExistence(timeout: 10))
        app.buttons["inbox.browseMenu"].tap()
        assertViewOptionsOnly(in: app)
        capture("view-menu-four-options", app)
        app.buttons["browse.sessions"].tap()
        XCTAssertEqual(app.staticTexts["inbox.title"].label, "Sessions")
        XCTAssertTrue(app.buttons["inbox.delete"].exists)
        XCTAssertTrue(app.buttons["inbox.search"].exists)
    }

    func test03AccessibleChineseViewMenuAndMetadata() {
        let app = open(large: true, chinese: true)
        let menu = app.buttons["inbox.browseMenu"]
        XCTAssertTrue(menu.waitForExistence(timeout: 10))
        menu.tap()
        assertViewOptionsOnly(in: app)
        for title in ["消息", "会话", "项目", "Agent"] {
            XCTAssertTrue(app.buttons[title].isHittable)
        }
        capture("view-menu-four-options-large-zh", app)
        app.buttons["browse.workspaces"].tap()
        XCTAssertEqual(app.staticTexts["inbox.title"].label, "项目")
        XCTAssertTrue(menu.isHittable)
    }

    private func assertViewOptionsOnly(in app: XCUIApplication) {
        for mode in ["messages", "sessions", "workspaces", "sources"] {
            XCTAssertTrue(app.buttons["browse." + mode].waitForExistence(timeout: 5))
        }
        for id in ["filter.all", "filter.unread", "filter.pending", "inbox.selectGroups", "inbox.merge"] {
            XCTAssertFalse(app.buttons[id].exists)
        }
        XCTAssertFalse(app.buttons["Mark all messages as read"].exists)
        XCTAssertFalse(app.buttons["全部标为已读"].exists)
        XCTAssertFalse(app.staticTexts["Filter notifications"].exists)
    }

    func test04LegacyQuestionsAreReadOnly() {
        var app = open(id: "ntf_soon")
        let options = app.staticTexts["Options from the agent"]
        XCTAssertTrue(options.waitForExistence(timeout: 10))
        reveal(options, in: app)
        XCTAssertTrue(app.staticTexts["1. approve"].exists)
        XCTAssertTrue(app.staticTexts["2. reject"].exists)
        XCTAssertFalse(app.buttons["Approve"].exists)
        XCTAssertFalse(app.buttons["Reject"].exists)
        XCTAssertFalse(app.buttons["question.jumpToAnswer"].exists)
        capture("legacy-choice-read-only", app)
        app.terminate()
        app = open(id: "ntf_later")
        XCTAssertTrue(app.staticTexts["Meow V1 only delivers notifications. Continue in the agent on your computer."].waitForExistence(timeout: 10))
        XCTAssertFalse(app.descendants(matching: .any).matching(identifier: "reply.field").firstMatch.exists)
        XCTAssertFalse(app.buttons["reply.send"].exists)
        capture("legacy-text-read-only", app)
        assertNoRepliesSubmitted()
    }

    private func assertNoRepliesSubmitted() {
        let done = expectation(description: "no response API calls")
        URLSession.shared.dataTask(with: URL(string: server + "/fixture/stats")!) { data, _, error in
            XCTAssertNil(error)
            let value = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Int] }
            XCTAssertEqual(value?["responses"], 0)
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 5)
    }

    func test05SettingsAndServerCancellation() {
        let app = open()
        XCTAssertTrue(app.tabBars.buttons["Settings"].waitForExistence(timeout: 10))
        app.tabBars.buttons["Settings"].tap()
        XCTAssertFalse(app.buttons["settings.advanced"].exists)
        XCTAssertFalse(app.buttons["settings.computer"].exists)
        XCTAssertFalse(app.buttons["settings.about"].exists)
        XCTAssertFalse(app.buttons["connection.check"].exists)
        XCTAssertTrue(app.buttons["notifications.sound"].exists)
        capture("settings-home", app)
        let test = app.buttons["Send a test notification"]
        reveal(test, in: app); test.tap()
        XCTAssertTrue(app.staticTexts["Sent. Notifications are off, so it's only in your inbox."].waitForExistence(timeout: 10))
        let server = app.buttons["settings.server"]
        reveal(server, in: app); server.tap()
        XCTAssertTrue(app.textFields["server.address"].waitForExistence(timeout: 5))
        app.buttons["Cancel"].tap()
        XCTAssertTrue(app.buttons["settings.rotateKey"].exists)
        XCTAssertFalse(app.buttons["Create new Device Key…"].exists)
        XCTAssertFalse(app.alerts.firstMatch.exists)
    }

    func testSettingsNavigationAndSoundPersistence() {
        let app = open()
        XCTAssertTrue(app.tabBars.buttons["Settings"].waitForExistence(timeout: 10))
        app.tabBars.buttons["Settings"].tap()
        let sound = app.buttons["notifications.sound"]
        XCTAssertTrue(sound.exists)
        sound.tap(); app.buttons["System default"].tap()
        XCTAssertFalse(app.buttons["notifications.previewSound"].exists)
        XCTAssertTrue(sound.label.contains("System default"))
        app.terminate(); app.launch()
        app.tabBars.buttons["Settings"].tap()
        XCTAssertTrue(sound.waitForExistence(timeout: 5))
        XCTAssertTrue(sound.label.contains("System default"), "sound preference must survive relaunch")
        sound.tap(); app.buttons["Cat sound"].tap()
        XCTAssertTrue(sound.label.contains("Cat sound"))
        XCTAssertEqual(app.alerts.count, 0, "selecting the cat sound should preview it successfully")
        XCTAssertTrue(app.navigationBars["Settings"].exists)
    }

    func testSettingsDeviceKeyResetConfirmationAndRecovery() {
        scenario("key-reset")
        let app = open()
        XCTAssertTrue(app.tabBars.buttons["Settings"].waitForExistence(timeout: 10))
        app.tabBars.buttons["Settings"].tap()
        let reset = app.buttons["settings.rotateKey"]
        reveal(reset, in: app)
        XCTAssertTrue(reset.label.contains("Reset Device Key"))
        capture("settings-reset-key-row", app)
        reset.tap()
        let confirm = app.alerts["Reset Device Key?"]
        XCTAssertTrue(confirm.waitForExistence(timeout: 3))
        XCTAssertTrue(confirm.staticTexts["Your old Device Key will stop working. Sign in to Meow again on your computer with the new key. Your messages will be kept."].exists)
        capture("settings-reset-key-confirmation", app)
        confirm.buttons["Cancel"].tap()
        assertKeyRequests(attempts: 0, revocations: 0)
        reset.tap()
        confirm.buttons["Reset key"].tap()
        XCTAssertTrue(app.alerts.firstMatch.waitForExistence(timeout: 5))
        XCTAssertTrue(app.alerts.firstMatch.staticTexts["Try creating the key again."].exists)
        capture("settings-reset-key-failure", app)
        app.alerts.firstMatch.buttons["OK"].tap()
        sendKeyResetTest(in: app)
        assertSenderKeyUse(old: 1, new: 0)
        reveal(reset, in: app); reset.tap()
        confirm.buttons["Reset key"].tap()
        let success = app.staticTexts["New Device Key created. Update it in your CLI."]
        XCTAssertTrue(success.waitForExistence(timeout: 8))
        assertKeyRequests(attempts: 2, revocations: 1)
        let toastGone = expectation(for: NSPredicate(format: "exists == false"), evaluatedWith: success)
        wait(for: [toastGone], timeout: 6)
        sendKeyResetTest(in: app)
        assertSenderKeyUse(old: 1, new: 1)
        XCTAssertTrue(app.tabBars.buttons["Inbox"].exists, "reset must not restart onboarding")
        app.terminate(); app.launch()
        app.tabBars.buttons["Settings"].tap()
        sendKeyResetTest(in: app)
        assertSenderKeyUse(old: 1, new: 2)
        capture("settings-reset-key-complete", app)
    }

    func testSettingsDeviceKeyResetChineseConfirmation() {
        let app = open(large: true, chinese: true)
        XCTAssertTrue(app.tabBars.buttons["设置"].waitForExistence(timeout: 10))
        app.tabBars.buttons["设置"].tap()
        let reset = app.buttons["settings.rotateKey"]
        reveal(reset, in: app)
        capture("settings-reset-key-row-large-zh", app)
        reset.tap()
        let confirm = app.alerts["重置 Device Key？"]
        XCTAssertTrue(confirm.waitForExistence(timeout: 3))
        XCTAssertTrue(confirm.staticTexts["旧 Device Key 将失效。请在电脑上使用新 Key 重新登录 Meow。消息历史会保留。"].exists)
        XCTAssertTrue(confirm.buttons["重置密钥"].isHittable)
        capture("settings-reset-key-confirmation-large-zh", app)
        confirm.buttons["取消"].tap()
        assertKeyRequests(attempts: 0, revocations: 0)
    }

    private func assertKeyRequests(attempts: Int, revocations: Int) {
        let done = expectation(description: "key request counts")
        URLSession.shared.dataTask(with: URL(string: server + "/fixture/stats")!) { data, _, error in
            XCTAssertNil(error)
            let stats = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Int] }
            XCTAssertEqual(stats?["key_attempts"], attempts)
            XCTAssertEqual(stats?["key_revocations"], revocations)
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 5)
    }

    private func sendKeyResetTest(in app: XCUIApplication) {
        let button = app.buttons["Send a test notification"]
        reveal(button, in: app); button.tap()
        let sent = app.staticTexts["Sent. Notifications are off, so it's only in your inbox."]
        XCTAssertTrue(sent.waitForExistence(timeout: 8))
        let gone = expectation(for: NSPredicate(format: "exists == false"), evaluatedWith: sent)
        wait(for: [gone], timeout: 6)
    }

    private func assertSenderKeyUse(old: Int, new: Int) {
        let done = expectation(description: "sender key actually used by the app")
        URLSession.shared.dataTask(with: URL(string: server + "/fixture/stats")!) { data, _, error in
            XCTAssertNil(error)
            let stats = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Int] }
            XCTAssertEqual(stats?["sender_key_old"], old)
            XCTAssertEqual(stats?["sender_key_new"], new)
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 5)
    }

    func testSettingsLargeChineseNavigation() {
        var app = open(chinese: true)
        XCTAssertTrue(app.tabBars.buttons["设置"].waitForExistence(timeout: 10))
        app.tabBars.buttons["设置"].tap()
        XCTAssertTrue(app.buttons["settings.focusHelp"].exists)
        capture("settings-home-zh", app)
        reveal(app.buttons["settings.shareSetup"], in: app)
        app.terminate()
        app = open(large: true, chinese: true)
        XCTAssertTrue(app.tabBars.buttons["设置"].waitForExistence(timeout: 10))
        app.tabBars.buttons["设置"].tap()
        capture("settings-home-large-zh", app)
        for id in ["settings.focusHelp", "notifications.sound", "settings.server", "settings.deviceKey", "settings.shareSetup", "settings.rotateKey"] {
            reveal(app.buttons[id], in: app)
        }
        capture("settings-connection-large-zh", app)
        XCTAssertTrue(app.navigationBars["设置"].exists, "all settings remain on one page")
        let showPrompt = app.buttons["settings.showPrompt"]
        reveal(showPrompt, in: app)
        XCTAssertFalse(app.staticTexts["settings.agentPrompt"].exists)
        capture("settings-help-large-zh", app)
        showPrompt.tap()
        let copyPrompt = app.buttons["settings.copyPrompt"]
        XCTAssertTrue(copyPrompt.waitForExistence(timeout: 5))
        XCTAssertTrue(copyPrompt.isHittable, "copy stays visible even with accessibility text")
        capture("settings-prompt-large-zh", app)
        copyPrompt.tap()
        app.buttons["完成"].tap()
        XCTAssertFalse(app.staticTexts["settings.agentPrompt"].exists)
    }

    func testSettingsFocusHelpAndAgentPrompt() {
        for chinese in [false, true] {
            let app = open(chinese: chinese)
            XCTAssertTrue(app.tabBars.buttons[chinese ? "设置" : "Settings"].waitForExistence(timeout: 10))
            app.tabBars.buttons[chinese ? "设置" : "Settings"].tap()
            app.buttons["settings.focusHelp"].tap()
            let alert = app.alerts.firstMatch
            XCTAssertTrue(alert.waitForExistence(timeout: 5))
            XCTAssertTrue(alert.buttons[chinese ? "打开通知设置" : "Open notification settings"].exists)
            capture(chinese ? "settings-focus-guide-zh" : "settings-focus-guide", app)
            if chinese {
                alert.buttons["取消"].tap()
            } else {
                alert.buttons["Open notification settings"].tap()
                let settings = XCUIApplication(bundleIdentifier: "com.apple.Preferences")
                XCTAssertTrue(settings.wait(for: .runningForeground, timeout: 5))
                app.activate()
            }
            let docs = app.descendants(matching: .any).matching(identifier: "settings.helpDocs").firstMatch
            reveal(docs, in: app)
            XCTAssertTrue(docs.exists)
            XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "settings.helpAPI").firstMatch.exists)
            let showPrompt = app.buttons["settings.showPrompt"]
            reveal(showPrompt, in: app)
            XCTAssertFalse(app.staticTexts["settings.agentPrompt"].exists)
            capture(chinese ? "settings-help-zh" : "settings-help", app)
            showPrompt.tap()
            let copy = app.buttons["settings.copyPrompt"]
            XCTAssertTrue(copy.waitForExistence(timeout: 5))
            let prompt = app.staticTexts["settings.agentPrompt"]
            XCTAssertTrue(prompt.label.contains("meow doctor"))
            XCTAssertTrue(prompt.label.contains("skills/meow/SKILL.md"))
            XCTAssertFalse(prompt.label.contains("meow_disposable_ui_fixture"))
            capture(chinese ? "settings-prompt-zh" : "settings-prompt", app)
            let expectedPrompt = prompt.label
            copy.tap()
            app.buttons[chinese ? "完成" : "Done"].tap()
            XCTAssertFalse(app.staticTexts["settings.agentPrompt"].exists)
            assertCopiedText(expectedPrompt, in: app, chinese: chinese)
            app.terminate()
        }
    }

    func testSettingsConnectionFailureAndRecovery() {
        var app = open()
        XCTAssertTrue(app.tabBars.buttons["Settings"].waitForExistence(timeout: 10))
        app.tabBars.buttons["Settings"].tap()
        XCTAssertFalse(app.buttons["connection.check"].exists)
        scenario("connection-error")
        app.terminate(); app = open()
        app.tabBars.buttons["Settings"].tap()
        let warning = app.descendants(matching: .any).matching(identifier: "settings.connectionWarning").firstMatch
        XCTAssertTrue(warning.waitForExistence(timeout: 10))
        capture("settings-connection-warning", app)
        let reregister = app.buttons["server.reregister"]
        reveal(reregister, in: app); reregister.tap()
        app.buttons["Continue"].tap()
        XCTAssertTrue(app.alerts["Re-register this server?"].waitForExistence(timeout: 5))
        app.alerts.buttons["Cancel"].tap(); app.buttons["Cancel"].tap()
        scenario("activity")
        let retry = app.buttons["connection.check"]
        reveal(retry, in: app); retry.tap()
        let cleared = XCTNSPredicateExpectation(predicate: NSPredicate(format: "exists == false"), object: warning)
        XCTAssertEqual(XCTWaiter.wait(for: [cleared], timeout: 10), .completed)
        XCTAssertFalse(retry.exists)
        XCTAssertFalse(reregister.exists)
    }

    private func scenario(_ name: String) {
        let done = expectation(description: "scenario ready")
        URLSession.shared.dataTask(with: URL(string: server + "/fixture/scenario?name=" + name)!) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 5)
    }

    func testCompactNotificationCategories() {
        scenario("compact-categories")
        var app = open()
        let row = app.descendants(matching: .any).matching(identifier: "notification.ntf_result").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        XCTAssertTrue(row.label.contains("Refine notification layout"))
        XCTAssertLessThan(row.frame.height, 120, "standard text should fit the compact three-line row")
        XCTAssertTrue(app.staticTexts["Release preparation"].exists, "the session is the inbox headline")
        XCTAssertTrue(app.staticTexts["meow · Codex"].exists, "project precedes agent on the second line")
        XCTAssertFalse(app.staticTexts["New result"].exists, "category text is represented by its icon")
        XCTAssertFalse(app.staticTexts["Needs your attention"].exists)
        XCTAssertFalse(app.staticTexts["Build 19 is ready"].exists)
        capture("compact-categories", app)
        row.tap()
        let heading = app.staticTexts["notification.heading"]
        XCTAssertTrue(heading.waitForExistence(timeout: 5))
        XCTAssertEqual(heading.label, "💬 Refine notification layout")
        XCTAssertFalse(app.staticTexts["New result · meow"].exists, "the event title must not be repeated above the body")
        app.buttons["Details"].tap()
        let originalTitle = app.descendants(matching: .any).matching(identifier: "notification.originalTitle").firstMatch
        XCTAssertTrue(originalTitle.waitForExistence(timeout: 5))
        // LabeledContent exposes one accessibility row, rather than a separate
        // StaticText for its value. Verify the original title on that row.
        XCTAssertTrue(originalTitle.label.contains("New result · meow") ||
                      (originalTitle.value as? String)?.contains("New result · meow") == true,
                      "the original title remains available in expanded details: \(originalTitle.debugDescription)")
        capture("detail-original-title", app)
        app.terminate()
        app = open(large: true, chinese: true)
        let largeRow = app.descendants(matching: .any).matching(identifier: "notification.ntf_result").firstMatch
        XCTAssertTrue(largeRow.waitForExistence(timeout: 10))
        XCTAssertTrue(largeRow.isHittable)
        XCTAssertTrue(largeRow.label.contains("Refine notification layout"))
        capture("compact-categories-accessible", app)
    }

    func test09NotificationBodyFormatting() {
        scenario("formatting")
        var app = open(id: "ntf_envelope", chinese: true)
        XCTAssertTrue(app.staticTexts["收到了，自动推送正常了"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.staticTexts["{\"suggestion\":\"收到了，自动推送正常了\"}"].exists)
        capture("body-json-envelope", app)
        app.buttons["详细信息"].tap()
        let original = app.buttons["notification.original"]
        reveal(original, in: app); original.tap()
        XCTAssertTrue(app.staticTexts["{\"suggestion\":\"收到了，自动推送正常了\"}"].exists)
        capture("body-original-json", app)
        app.terminate()

        app = open(id: "ntf_markdown", chinese: true)
        XCTAssertTrue(app.staticTexts["推送已恢复"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["Markdown 正常显示"].exists)
        XCTAssertFalse(app.staticTexts["## 推送已恢复"].exists)
        capture("body-markdown", app)
        reveal(app.staticTexts["Hook"], in: app)
        capture("body-markdown-table", app)
        app.terminate()

        app = open(id: "ntf_json", chinese: true)
        XCTAssertTrue(app.descendants(matching: .any)["notification.body"].waitForExistence(timeout: 10))
        let json = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "9007199254740993")).firstMatch
        XCTAssertTrue(json.waitForExistence(timeout: 5))
        XCTAssertTrue(json.label.contains("\n"))
        capture("body-json-formatted", app)
        app.terminate()

        app = open(id: "ntf_markdown", large: true, chinese: true)
        XCTAssertTrue(app.staticTexts["推送已恢复"].waitForExistence(timeout: 10))
        capture("body-markdown-accessible", app)
        reveal(app.staticTexts["Markdown 正常显示"], in: app)
        capture("body-markdown-accessible-list", app)
    }

    func test10EmptyTableHeaderKeepsColumnsAligned() {
        scenario("empty-table-header")
        let app = open(id: "ntf_info", chinese: true)
        let bark = app.staticTexts["Bark"]
        let meow = app.staticTexts.matching(NSPredicate(format: "label == %@ AND identifier BEGINSWITH %@", "Meow", "notification.table.")).firstMatch
        XCTAssertTrue(bark.waitForExistence(timeout: 10))
        XCTAssertTrue(meow.exists)
        let firstColumn = app.staticTexts["服务端保存正文"]
        let secondColumn = app.staticTexts["不保存"]
        let thirdColumn = app.staticTexts["保存到 SQLite"]
        XCTAssertGreaterThan(bark.frame.minX, firstColumn.frame.minX + 40)
        XCTAssertEqual(bark.frame.minX, secondColumn.frame.minX, accuracy: 1)
        XCTAssertEqual(meow.frame.minX, thirdColumn.frame.minX, accuracy: 1)
        XCTAssertEqual(app.staticTexts["最后一列"].frame.minX, thirdColumn.frame.minX, accuracy: 1)
        let longCell = app.staticTexts["当前设计同时删除服务器记录与本地缓存"]
        XCTAssertLessThanOrEqual(longCell.frame.width, 241)
        XCTAssertGreaterThan(longCell.frame.height, thirdColumn.frame.height)
        capture("empty-table-header-zh", app)
        app.terminate()

        let large = open(id: "ntf_info", large: true, chinese: true)
        XCTAssertTrue(large.staticTexts["Bark"].waitForExistence(timeout: 10))
        let firstLargeRow = large.staticTexts["服务端保存正文"]
        reveal(firstLargeRow, in: large)
        XCTAssertLessThan(firstLargeRow.frame.maxY, large.staticTexts["手机查看历史"].frame.minY)
        XCTAssertLessThan(large.staticTexts["手机查看历史"].frame.maxY, large.staticTexts["服务端补拉历史"].frame.minY)
        capture("empty-table-header-large-zh", large)
    }

    func test06UrgentQuestionBeyondFirstPageAppearsFirst() {
        scenario("pagination")
        let app = open()
        let urgent = app.descendants(matching: .any).matching(identifier: "notification.ntf_soon").firstMatch
        let later = app.descendants(matching: .any).matching(identifier: "notification.ntf_later").firstMatch
        XCTAssertTrue(urgent.waitForExistence(timeout: 10))
        XCTAssertTrue(urgent.isHittable)
        XCTAssertLessThan(urgent.frame.minY, later.frame.minY)
        capture("urgent-beyond-first-page", app)
    }

    func test07HistoricalAnswersRemainReadable() {
        scenario("answered")
        let app = open(id: "ntf_soon")
        XCTAssertTrue(app.staticTexts["Historical decision"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["Approve"].exists)
        XCTAssertFalse(app.buttons["Reject"].exists)
        XCTAssertFalse(app.buttons["reply.send"].exists)
        capture("historical-answer-read-only", app)
        assertNoRepliesSubmitted()
    }

    func test08CompactHeaderSearchAndViewMenu() {
        scenario("activity")
        let app = open()
        browse("messages", in: app)
        let row = app.descendants(matching: .any).matching(identifier: "notification.ntf_info").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        let navigation = app.navigationBars.firstMatch
        let firstRow = app.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH %@", "notification.")).firstMatch
        XCTAssertLessThan(firstRow.frame.minY - navigation.frame.maxY, 48,
                          "the first message should follow the compact navigation bar")
        XCTAssertFalse(app.staticTexts["Other notifications"].exists)
        XCTAssertFalse(app.textFields["inbox.searchField"].exists)
        XCTAssertFalse(app.buttons["inbox.markRead"].exists)
        capture("compact-inbox", app)

        app.buttons["inbox.search"].tap()
        let field = app.textFields["inbox.searchField"]
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.typeText("unmatched search")
        let gone = expectation(for: NSPredicate(format: "exists == false"), evaluatedWith: row)
        wait(for: [gone], timeout: 10)
        capture("compact-inbox-search", app)
        app.buttons["inbox.cancelSearch"].tap()
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        XCTAssertFalse(field.exists)

        app.buttons["inbox.browseMenu"].tap()
        assertViewOptionsOnly(in: app)
        app.buttons["browse.messages"].tap()
        XCTAssertTrue(row.waitForExistence(timeout: 10))
    }
    private func browse(_ mode: String, in app: XCUIApplication) {
        let menu = app.buttons["inbox.browseMenu"]
        XCTAssertTrue(menu.waitForExistence(timeout: 10)); menu.tap()
        let option = app.buttons["browse." + mode]
        XCTAssertTrue(option.waitForExistence(timeout: 5)); option.tap()
    }

    func test11NotificationDetailHeaderAndAccessibleChineseLayout() {
        scenario("detail-header")
        var app = open(id: "ntf_detail_result", chinese: true)
        for large in [false, true] {
            if large {
                app.terminate()
                app = open(id: "ntf_detail_result", large: true, chinese: true)
            }
            let heading = app.staticTexts["notification.heading"]
            XCTAssertTrue(heading.waitForExistence(timeout: 10))
            XCTAssertEqual(heading.label, "💬 meow 用户 onboarding 流程")
            let metadata = app.staticTexts["notification.metadata"]
            let date = app.staticTexts["notification.date"]
            let body = app.staticTexts["PR #5 方向是对的，大部分改动可以合并。"]
            XCTAssertEqual(metadata.label, "meow-repo · Claude Code")
            XCTAssertTrue(date.exists)
            XCTAssertFalse(date.label.isEmpty)
            XCTAssertGreaterThanOrEqual(metadata.frame.minY, heading.frame.maxY)
            XCTAssertGreaterThanOrEqual(date.frame.minY, metadata.frame.maxY)
            XCTAssertGreaterThanOrEqual(body.frame.minY, date.frame.maxY)
            XCTAssertEqual(heading.frame.minX, metadata.frame.minX)
            XCTAssertEqual(metadata.frame.minX, body.frame.minX)
            XCTAssertFalse(app.staticTexts["New result · meow-repo"].exists)
            XCTAssertFalse(app.staticTexts["Claude Code · meow-repo"].exists)
            capture(large ? "detail-session-header-large-zh" : "detail-session-header-zh", app)
            reveal(app.staticTexts["合并前建议修"], in: app)
            XCTAssertTrue(app.staticTexts["合并前建议修"].exists, "the Markdown body remains readable")
        }
    }

    func test12NotificationDetailCategoriesAndMissingSessionFallbacks() {
        scenario("detail-header")
        for (id, title, metadata) in [
            ("attention", "🔔 发布前检查", "meow-repo · Codex"),
            ("failure", "⚠️ 检查服务连接", "meow-repo · Codex"),
            ("id_only", "💬 Session · thread-d…l-42", "meow-repo · Codex"),
            ("service", "💬 Backup completed", "ops · Backup service"),
            ("legacy", "💬 Service is healthy", "Monitor"),
        ] {
            let app = open(id: "ntf_detail_" + id)
            let heading = app.staticTexts["notification.heading"]
            XCTAssertTrue(heading.waitForExistence(timeout: 10))
            XCTAssertEqual(heading.label, title)
            XCTAssertEqual(app.staticTexts["notification.metadata"].label, metadata)
            XCTAssertTrue(app.staticTexts["notification.date"].exists)
            XCTAssertTrue(app.staticTexts["PR #5 方向是对的，大部分改动可以合并。"].exists)
            capture("detail-header-" + id, app)
            app.terminate()
        }
    }

    func test13HeaderOrderIsViewsDeleteSearch() {
        scenario("grouping")
        for chinese in [false, true] {
            let app = open(large: chinese, chinese: chinese)
            browse("messages", in: app)
            let views = app.buttons["inbox.browseMenu"]
            let search = app.buttons["inbox.search"]
            let trash = app.buttons["inbox.delete"]
            XCTAssertTrue(views.isHittable)
            XCTAssertTrue(search.isHittable)
            XCTAssertLessThan(views.frame.midX, trash.frame.midX)
            XCTAssertLessThan(trash.frame.midX, search.frame.midX)
            views.tap()
            for mode in ["messages", "sessions", "workspaces", "sources"] {
                XCTAssertTrue(app.buttons["browse." + mode].exists, "every view is available directly from the header")
            }
            XCTAssertTrue(app.buttons["browse.sources"].label.contains("Agent"))
            XCTAssertFalse(app.buttons["Sources"].exists)
            XCTAssertFalse(app.buttons["来源"].exists)
            capture(chinese ? "header-views-menu-large-zh" : "header-views-menu", app)
            app.buttons["browse.sources"].tap()
            XCTAssertEqual(views.value as? String, "Agent")
            capture(chinese ? "header-agent-large-zh" : "header-agent", app)
            search.tap()
            let field = app.textFields["inbox.searchField"]
            XCTAssertTrue(field.waitForExistence(timeout: 5)); field.tap(); field.typeText("result")
            browse("sessions", in: app)
            XCTAssertEqual(views.value as? String, chinese ? "会话" : "Sessions")
            XCTAssertEqual(field.value as? String, "result", "switching views preserves the active search")
            capture(chinese ? "header-views-search-large-zh" : "header-views-search", app)
            app.buttons["inbox.cancelSearch"].tap()
            browse("messages", in: app)
            app.terminate()
        }
    }

    func test20GroupedViewsRetainNavigationWithoutSelectionActions() {
        scenario("grouping")
        let app = open()
        browse("sessions", in: app)
        XCTAssertTrue(app.staticTexts["Deploy"].firstMatch.waitForExistence(timeout: 10))
        capture("grouping-sessions", app)
        app.buttons["inbox.browseMenu"].tap()
        assertViewOptionsOnly(in: app)
        app.buttons["browse.sessions"].tap()
        app.staticTexts["Deploy"].firstMatch.tap()
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "notification.ntf_group_a1").firstMatch.waitForExistence(timeout: 10))
        XCTAssertTrue(app.tabBars.firstMatch.exists)
        capture("grouping-group-messages", app)
        app.navigationBars.buttons.element(boundBy: 0).tap()
        browse("workspaces", in: app)
        XCTAssertTrue(app.staticTexts["meow"].firstMatch.waitForExistence(timeout: 5))
        XCTAssertEqual(app.staticTexts.matching(identifier: "meow").count, 1)
        capture("grouping-projects", app)
        browse("messages", in: app)
    }

    func test21GroupSearchReadsOnlyMatchingMessagesAndModeSurvivesRestart() {
        scenario("grouping")
        let app = open()
        browse("sessions", in: app)
        XCTAssertTrue(app.staticTexts["Deploy"].firstMatch.waitForExistence(timeout: 10))
        app.buttons["inbox.search"].tap()
        let rootSearch = app.textFields["inbox.searchField"]
        XCTAssertTrue(rootSearch.waitForExistence(timeout: 5)); rootSearch.typeText("needle")
        app.staticTexts["Deploy"].firstMatch.tap()
        XCTAssertTrue(app.buttons["group.search"].waitForExistence(timeout: 5))
        let field = app.textFields["group.searchField"]
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        let row = app.descendants(matching: .any).matching(identifier: "notification.ntf_group_a1").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 5))
        let other = app.descendants(matching: .any).matching(identifier: "notification.ntf_group_a2").firstMatch
        let gone = expectation(for: NSPredicate(format: "exists == false"), evaluatedWith: other)
        wait(for: [gone], timeout: 5)
        app.buttons["group.actions"].tap()
        let mark = app.buttons["group.markRead"]
        XCTAssertFalse(mark.isEnabled, "entry already marks the matching snapshot read")
        app.tap()
        let done = expectation(description: "only matching message read")
        // Allow the local action to commit before querying the independent fixture.
        DispatchQueue.global().asyncAfter(deadline: .now() + 1) {
            URLSession.shared.dataTask(with: URL(string: self.server + "/fixture/stats")!) { data, _, error in
                XCTAssertNil(error)
                let value = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Int] }
                XCTAssertEqual(value?["unread"], 4)
                done.fulfill()
            }.resume()
        }
        wait(for: [done], timeout: 5)
        capture("grouping-matching-read", app)
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.textFields["inbox.searchField"].waitForExistence(timeout: 5), "the inherited search must remain visible on return")
        XCTAssertTrue(app.staticTexts["0 matching unread messages"].waitForExistence(timeout: 5))
        capture("grouping-search-context", app)
        app.terminate(); app.launch()
        XCTAssertTrue(app.staticTexts["Deploy"].firstMatch.waitForExistence(timeout: 10))
        capture("grouping-restored-mode", app)
        browse("messages", in: app)
    }

    func test26GroupEntryMarksItsMessagesRead() {
        scenario("grouping")
        let app = open()
        browse("sessions", in: app)
        let deploy = app.staticTexts["Deploy"].firstMatch
        XCTAssertTrue(deploy.waitForExistence(timeout: 10)); deploy.tap()
        XCTAssertTrue(app.buttons["group.search"].waitForExistence(timeout: 5))
        assertInboxStats(unread: 3, total: 5)
        capture("inbox-group-auto-read", app)
    }

    func test27InboxReadHeaderAndGroupSwipeActions() {
        scenario("grouping")
        let app = open()
        browse("sessions", in: app)
        let row = app.staticTexts["Deploy"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        row.swipeLeft()
        XCTAssertTrue(app.buttons["Mark as read"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["Delete"].exists)
        capture("inbox-group-swipe", app)
        app.buttons["Mark as read"].tap()
        assertInboxStats(unread: 3, total: 5)
        row.swipeLeft(); app.buttons["Delete"].tap()
        let confirmation = app.alerts["Delete group messages?"]
        XCTAssertTrue(confirmation.waitForExistence(timeout: 3))
        capture("inbox-group-delete-confirmation", app)
        confirmation.buttons["Cancel"].tap()
        assertInboxStats(unread: 3, total: 5)
        row.swipeLeft(); app.buttons["Delete"].tap()
        confirmation.buttons["Delete"].tap()
        assertInboxStats(unread: 3, total: 3)
        let gone = expectation(for: NSPredicate(format: "exists == false"), evaluatedWith: row)
        wait(for: [gone], timeout: 5)
        XCTAssertFalse(app.buttons["inbox.markRead"].exists)
        capture("inbox-no-header-read-action", app)
    }

    func test28GroupVisitDoesNotReadNewArrivalOnRefreshOrDetailReturn() {
        scenario("grouping")
        let app = open()
        browse("sessions", in: app)
        let deploy = app.staticTexts["Deploy"].firstMatch
        XCTAssertTrue(deploy.waitForExistence(timeout: 10)); deploy.tap()
        assertInboxStats(unread: 3, total: 5)
        let appended = expectation(description: "new notification")
        URLSession.shared.dataTask(with: URL(string: server + "/fixture/append-group-message")!) { _, _, _ in appended.fulfill() }.resume()
        wait(for: [appended], timeout: 5)
        let list = app.collectionViews.firstMatch
        let top = list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.15))
        let bottom = list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.85))
        top.press(forDuration: 0.2, thenDragTo: bottom)
        let incoming = app.descendants(matching: .any).matching(identifier: "notification.ntf_incoming").firstMatch
        XCTAssertTrue(incoming.waitForExistence(timeout: 8))
        assertInboxStats(unread: 4, total: 6)
        let old = app.descendants(matching: .any).matching(identifier: "notification.ntf_group_a1").firstMatch
        old.tap()
        XCTAssertTrue(app.navigationBars["Notification"].waitForExistence(timeout: 5))
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(incoming.waitForExistence(timeout: 5))
        assertInboxStats(unread: 4, total: 6)
        incoming.swipeLeft()
        XCTAssertTrue(app.buttons["Mark as read"].waitForExistence(timeout: 3))
        capture("inbox-new-message-swipe", app)
        app.buttons["Mark as read"].tap()
        assertInboxStats(unread: 3, total: 6)
        incoming.swipeLeft(); app.buttons["Delete"].tap()
        assertInboxStats(unread: 3, total: 5)
        capture("inbox-group-new-message-handled", app)
    }

    func test29MessageSwipeAndSearchGroupDeletionKeepUnmatchedHistory() {
        scenario("grouping")
        let app = open()
        browse("messages", in: app)
        let single = app.descendants(matching: .any).matching(identifier: "notification.ntf_group_legacy").firstMatch
        XCTAssertTrue(single.waitForExistence(timeout: 10)); single.swipeLeft()
        capture("inbox-message-swipe", app)
        app.buttons["Mark as read"].tap()
        assertInboxStats(unread: 4, total: 5)
        single.swipeLeft(); app.buttons["Delete"].tap()
        assertInboxStats(unread: 4, total: 4)
        browse("sessions", in: app)
        app.buttons["inbox.search"].tap()
        let search = app.textFields["inbox.searchField"]
        XCTAssertTrue(search.waitForExistence(timeout: 5)); search.typeText("needle")
        let deploy = app.staticTexts["Deploy"].firstMatch
        XCTAssertTrue(deploy.waitForExistence(timeout: 5)); deploy.swipeLeft()
        app.buttons["Delete"].tap()
        let confirmation = app.alerts["Delete group messages?"]
        XCTAssertTrue(confirmation.waitForExistence(timeout: 3))
        XCTAssertTrue(confirmation.staticTexts["Deploy — matching messages: 1. Permanently delete them from this server and this iPhone?"].exists)
        confirmation.buttons["Delete"].tap()
        assertInboxStats(unread: 3, total: 3)
        app.buttons["inbox.cancelSearch"].tap()
        XCTAssertTrue(deploy.waitForExistence(timeout: 5), "unmatched messages keep the group and its history")
        capture("inbox-search-delete-retains-group", app)
    }

    func test30ReadActionsLargeChinese() {
        scenario("grouping")
        let app = open(large: true, chinese: true)
        browse("sessions", in: app)
        XCTAssertFalse(app.buttons["inbox.markRead"].exists)
        let deploy = app.staticTexts["Deploy"].firstMatch
        let row = app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@ AND label CONTAINS %@", "inbox.group.", "Deploy")).firstMatch
        reveal(row, in: app)
        XCTAssertTrue(deploy.waitForExistence(timeout: 10)); row.swipeLeft()
        XCTAssertTrue(app.buttons["标为已读"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["删除"].isHittable)
        capture("inbox-group-swipe-large-zh", app)
        app.buttons["删除"].tap()
        let confirmation = app.alerts["删除分组消息？"]
        XCTAssertTrue(confirmation.waitForExistence(timeout: 3))
        XCTAssertTrue(confirmation.buttons["删除"].isHittable)
        capture("inbox-group-delete-large-zh", app)
        confirmation.buttons["取消"].tap()
        assertInboxStats(unread: 5, total: 5)
        XCTAssertFalse(app.buttons["inbox.markRead"].exists)
        capture("inbox-no-read-header-large-zh", app)
    }

    private func assertInboxStats(unread: Int, total: Int) {
        let done = expectation(description: "independent inbox state")
        DispatchQueue.global().async {
            var last: [String: Int]?
            for _ in 0..<50 {
                if let data = try? Data(contentsOf: URL(string: self.server + "/fixture/stats")!),
                   let stats = try? JSONSerialization.jsonObject(with: data) as? [String: Int] {
                    last = stats
                    if stats["unread"] == unread && stats["total"] == total { break }
                }
                Thread.sleep(forTimeInterval: 0.1)
            }
            XCTAssertEqual(last?["unread"], unread)
            XCTAssertEqual(last?["total"], total)
            XCTAssertEqual(last?["read_requests"], 0, "new app read actions must never call server read endpoints")
            done.fulfill()
        }
        wait(for: [done], timeout: 10)
    }

    func test22GroupedInboxLargeChineseText() {
        scenario("grouping")
        let app = open(large: true, chinese: true)
        browse("workspaces", in: app)
        XCTAssertTrue(app.staticTexts["meow"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["inbox.browseMenu"].isHittable)
        capture("grouping-large-zh", app)
        browse("messages", in: app)
    }

    func test23SourceGroupDetailBackAndSearchEmptyState() {
        scenario("grouping")
        let app = open()
        browse("sources", in: app)
        let source = app.staticTexts["Monitor"].firstMatch
        XCTAssertTrue(source.waitForExistence(timeout: 10))
        capture("grouping-sources", app)
        source.tap()
        let row = app.descendants(matching: .any).matching(identifier: "notification.ntf_group_legacy").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 5)); row.tap()
        XCTAssertTrue(app.staticTexts["legacy service result"].firstMatch.waitForExistence(timeout: 5))
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(row.waitForExistence(timeout: 5), "read messages remain visible without unread filtering")
        app.buttons["group.search"].tap()
        let query = app.textFields["group.searchField"]
        XCTAssertTrue(query.waitForExistence(timeout: 5)); query.tap(); query.typeText("no such message")
        XCTAssertTrue(app.staticTexts["No matching messages"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["group.actions"].exists, "empty groups keep their page until the user leaves")
        capture("grouping-empty", app)
        app.buttons["Clear search and filters"].tap()
        XCTAssertTrue(row.waitForExistence(timeout: 5))
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.buttons["inbox.browseMenu"].waitForExistence(timeout: 5))
        XCTAssertTrue(source.exists)
        browse("messages", in: app)
    }

    func test24SessionIDKeepsRenamesTogetherAndSameNamesApart() {
        scenario("session-identity")
        let app = open()
        browse("sessions", in: app)
        let titles = app.staticTexts.matching(identifier: "Renamed identity session")
        XCTAssertTrue(titles.firstMatch.waitForExistence(timeout: 10))
        XCTAssertEqual(titles.count, 2, "different IDs with the same title remain separate")
        capture("session-identity-distinct", app)
        app.buttons["inbox.search"].tap()
        let search = app.textFields["inbox.searchField"]
        XCTAssertTrue(search.waitForExistence(timeout: 5)); search.typeText("First unnamed identity result")
        let onlyOne = expectation(for: NSPredicate(format: "count == 1"), evaluatedWith: titles)
        wait(for: [onlyOne], timeout: 5)
        titles.firstMatch.tap()
        XCTAssertTrue(app.navigationBars["Renamed identity session"].waitForExistence(timeout: 5))
        app.buttons["Cancel"].firstMatch.tap()
        for id in ["first", "named", "renamed", "latest"] {
            let row = app.descendants(matching: .any).matching(identifier: "notification.ntf_identity_" + id).firstMatch
            XCTAssertTrue(row.waitForExistence(timeout: 5), "same ID's complete local history stays in one group")
        }
        XCTAssertFalse(app.descendants(matching: .any).matching(identifier: "notification.ntf_identity_other").firstMatch.exists)
        capture("session-identity-renamed-history", app)
        app.navigationBars.buttons.element(boundBy: 0).tap()
        browse("messages", in: app)
    }

    func test25PlainHeaderKeepsViewNamesAndLeadingAlignment() {
        scenario("grouping")
        for chinese in [false, true] {
            let app = open(large: chinese, chinese: chinese)
            var leadingX: CGFloat?
            for (mode, title) in [
                ("messages", chinese ? "消息" : "Messages"),
                ("sessions", chinese ? "会话" : "Sessions"),
                ("workspaces", chinese ? "项目" : "Projects"),
                ("sources", "Agent"),
            ] {
                browse(mode, in: app)
                let heading = app.staticTexts["inbox.title"]
                XCTAssertTrue(heading.waitForExistence(timeout: 5))
                XCTAssertEqual(heading.label, title)
                XCTAssertFalse(app.buttons["inbox.title"].exists)
                XCTAssertFalse(app.buttons["inbox.filterMenu"].exists)
                let views = app.buttons["inbox.browseMenu"]
                let trash = app.buttons["inbox.delete"]
                let search = app.buttons["inbox.search"]
                XCTAssertLessThan(heading.frame.minX, 40, "all modes use a leading title")
                XCTAssertLessThan(heading.frame.maxX, views.frame.minX, "title must not overlap actions")
                if let leadingX {
                    XCTAssertEqual(heading.frame.minX, leadingX, accuracy: 1)
                } else {
                    leadingX = heading.frame.minX
                }
                XCTAssertLessThan(views.frame.midX, trash.frame.midX)
                XCTAssertLessThan(trash.frame.midX, search.frame.midX)
                heading.tap()
                XCTAssertFalse(app.buttons["filter.unread"].exists, "the title does not open a menu")
                capture("plain-header-" + mode + (chinese ? "-large-zh" : "-en"), app)
            }
            app.buttons["inbox.browseMenu"].tap()
            assertViewOptionsOnly(in: app)
            app.buttons["browse.sources"].tap()
            XCTAssertEqual(app.staticTexts["inbox.title"].label, "Agent", "the title stays identical to the view name")
            browse("messages", in: app)
            app.terminate()
        }
    }

}
