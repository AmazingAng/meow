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
        let visibleArea = CGRect(x: app.frame.minX, y: app.frame.minY + 100,
                                 width: app.frame.width, height: app.frame.height - 240)
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

    func test01GuidedSetupAndBackNavigation() throws {
        let app = XCUIApplication()
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
        app.buttons["onboarding.back"].tap()
        XCTAssertTrue(app.buttons["onboarding.cliHelp"].waitForExistence(timeout: 5))
        app.buttons["Next"].tap()
        XCTAssertTrue(app.buttons["Done"].waitForExistence(timeout: 5))
        app.buttons["Done"].tap()
        XCTAssertTrue(app.buttons["inbox.filterMenu"].waitForExistence(timeout: 10))
    }

    func test02InboxHasOnlyAllAndUnreadFilters() {
        let app = open()
        let row = app.descendants(matching: .any).matching(identifier: "notification.ntf_info").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["inbox.pendingSummary"].exists)
        capture("notification-only-inbox", app)
        app.buttons["inbox.filterMenu"].tap()
        XCTAssertTrue(app.buttons["filter.all"].exists)
        XCTAssertTrue(app.buttons["filter.unread"].exists)
        XCTAssertFalse(app.buttons["filter.pending"].exists)
        app.buttons["filter.unread"].tap()
        XCTAssertFalse(app.descendants(matching: .any).matching(identifier: "notification.ntf_soon").firstMatch.exists)
    }

    func test03AccessibleChineseFilterAndMetadata() {
        let app = open(large: true, chinese: true)
        let menu = app.buttons["inbox.filterMenu"]
        XCTAssertTrue(menu.waitForExistence(timeout: 10))
        XCTAssertTrue(menu.isHittable)
        capture("inbox-large-zh", app)
        menu.tap()
        XCTAssertFalse(app.buttons["filter.pending"].exists)
        app.buttons["filter.unread"].tap()
        XCTAssertTrue(menu.isHittable)
        capture("unread-large-zh", app)
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

    func test05SettingsAndReregisterCancellation() {
        let app = open()
        XCTAssertTrue(app.tabBars.buttons["Settings"].waitForExistence(timeout: 10))
        app.tabBars.buttons["Settings"].tap()
        XCTAssertTrue(app.buttons["connection.check"].waitForExistence(timeout: 5))
        let sync = app.staticTexts["Inbox sync, Last sync succeeded"]
        XCTAssertLessThan(sync.frame.height, 90, "The sync status must not create an oversized empty row")
        capture("settings-status", app)
        let test = app.buttons["Send a test notification"]
        reveal(test, in: app); test.tap()
        XCTAssertTrue(app.staticTexts["Sent. Notifications are off, so it's only in your inbox."].waitForExistence(timeout: 10))
        let reregister = app.buttons["server.reregister"]
        reveal(reregister, in: app); reregister.tap()
        XCTAssertTrue(app.navigationBars["Re-register"].waitForExistence(timeout: 5))
        app.buttons["Continue"].tap()
        XCTAssertTrue(app.alerts["Re-register this server?"].waitForExistence(timeout: 5))
        capture("reregister-confirmation", app)
        app.alerts.buttons["Cancel"].tap(); app.buttons["Cancel"].tap()
        XCTAssertTrue(app.tabBars.buttons["Inbox"].exists)
    }

    private func scenario(_ name: String) {
        let done = expectation(description: "scenario ready")
        URLSession.shared.dataTask(with: URL(string: server + "/fixture/scenario?name=" + name)!) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 5)
    }

    private func selectFilter(_ name: String, in app: XCUIApplication) {
        let menu = app.buttons["inbox.filterMenu"]
        XCTAssertTrue(menu.waitForExistence(timeout: 10))
        menu.tap()
        app.buttons["filter.\(name)"].tap()
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

    func test08CompactHeaderSearchAndUnreadMenu() {
        scenario("activity")
        let app = open()
        let row = app.descendants(matching: .any).matching(identifier: "notification.ntf_info").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        let navigation = app.navigationBars.firstMatch
        XCTAssertLessThan(row.frame.minY - navigation.frame.maxY, 48,
                          "the first message should follow the compact navigation bar")
        XCTAssertFalse(app.staticTexts["Other notifications"].exists)
        XCTAssertFalse(app.textFields["inbox.searchField"].exists)
        XCTAssertFalse(app.buttons["Mark all as read"].exists)
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

        app.buttons["inbox.filterMenu"].tap()
        app.buttons["Mark all as read"].tap()
        selectFilter("unread", in: app)
        XCTAssertTrue(app.staticTexts["All caught up"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["inbox.filterMenu"].isHittable)
        selectFilter("all", in: app)
        XCTAssertTrue(row.waitForExistence(timeout: 10))
    }
}
