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
        XCTAssertTrue(app.staticTexts["Build and install"].exists)
        XCTAssertFalse(app.links["Download Meow CLI"].exists)
        capture("setup-cli-help", app)
        app.buttons["Done"].tap()
        app.buttons["Next"].tap()
        XCTAssertTrue(app.buttons["Skip for now"].waitForExistence(timeout: 5))
        app.buttons["onboarding.back"].tap()
        XCTAssertTrue(app.buttons["onboarding.cliHelp"].waitForExistence(timeout: 5))
        app.buttons["Next"].tap(); app.buttons["Skip for now"].tap()
        XCTAssertTrue(app.buttons["inbox.pendingSummary"].waitForExistence(timeout: 10))
    }

    func test02PendingPriorityAndFilter() {
        let app = open()
        let pending = app.descendants(matching: .any).matching(identifier: "notification.ntf_soon").firstMatch
        let later = app.descendants(matching: .any).matching(identifier: "notification.ntf_later").firstMatch
        XCTAssertTrue(pending.waitForExistence(timeout: 10))
        XCTAssertLessThan(pending.frame.minY, later.frame.minY)
        capture("inbox-priority", app)
        app.buttons["inbox.pendingSummary"].tap()
        XCTAssertTrue(pending.waitForExistence(timeout: 5))
        XCTAssertFalse(app.descendants(matching: .any).matching(identifier: "notification.ntf_info").firstMatch.exists)
    }

    func test03AccessibleChineseFilterAndMetadata() {
        let app = open(large: true, chinese: true)
        let menu = app.buttons["inbox.filterMenu"]
        XCTAssertTrue(menu.waitForExistence(timeout: 10))
        XCTAssertTrue(menu.isHittable)
        capture("inbox-large-zh", app)
        menu.tap()
        app.buttons["filter.pending"].tap()
        XCTAssertTrue(menu.isHittable)
        capture("pending-large-zh", app)
    }

    func test04LongApprovalAndReplyComposer() {
        var app = open(id: "ntf_soon")
        let jump = app.buttons["question.jumpToAnswer"]
        XCTAssertTrue(jump.waitForExistence(timeout: 10)); XCTAssertTrue(jump.isHittable)
        capture("long-approval-top", app)
        jump.tap()
        XCTAssertTrue(app.buttons["Reject"].isHittable)
        capture("long-approval-actions", app)
        app.buttons["Reject"].tap()
        XCTAssertTrue(app.staticTexts["Rejected"].waitForExistence(timeout: 10))
        XCTAssertFalse(jump.exists)
        app.terminate()
        app = open(id: "ntf_later")
        let field = app.descendants(matching: .any).matching(identifier: "reply.field").firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 10)); field.tap(); field.typeText("Prioritize the inbox")
        XCTAssertTrue(app.buttons["reply.send"].isHittable)
        capture("reply-keyboard", app)
        app.buttons["reply.send"].tap()
        XCTAssertTrue(app.staticTexts["You answered"].waitForExistence(timeout: 10))
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

    func test07PendingFilterUpdatesAfterAnswerAndExpiry() {
        scenario("answer")
        var app = open()
        XCTAssertTrue(app.buttons["filter.pending"].waitForExistence(timeout: 10))
        app.buttons["filter.pending"].tap()
        let row = app.descendants(matching: .any).matching(identifier: "notification.ntf_soon").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10)); row.tap()
        XCTAssertTrue(app.buttons["Reject"].waitForExistence(timeout: 5)); app.buttons["Reject"].tap()
        XCTAssertTrue(app.staticTexts["Rejected"].waitForExistence(timeout: 5))
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.staticTexts["Nothing waiting on you"].waitForExistence(timeout: 5))
        XCTAssertFalse(row.exists)
        capture("pending-after-answer", app)
        app.terminate()

        scenario("expiry")
        app = open()
        XCTAssertTrue(app.buttons["filter.pending"].waitForExistence(timeout: 10))
        app.buttons["filter.pending"].tap()
        let expiring = app.descendants(matching: .any).matching(identifier: "notification.ntf_soon").firstMatch
        XCTAssertTrue(expiring.waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Nothing waiting on you"].waitForExistence(timeout: 25))
        XCTAssertFalse(expiring.exists)
        XCTAssertFalse(app.buttons["filter.pending"].label.contains("1"), "the expired question must leave the filter count too")
        capture("pending-after-expiry", app)
    }
}
