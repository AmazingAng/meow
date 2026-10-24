import XCTest

/// Drives the real app against a running local server. Skipped unless
/// MEOW_UITEST_NOTIFICATION is set, so a plain `xcodebuild test` stays hermetic.
///
///   TEST_RUNNER_MEOW_UITEST_NOTIFICATION=ntf_… TEST_RUNNER_MEOW_UITEST_ANSWER="…" xcodebuild test …
final class AnswerFlowUITests: XCTestCase {
    func testReplyToQuestionFromDetail() throws {
        let env = ProcessInfo.processInfo.environment
        guard let id = env["MEOW_UITEST_NOTIFICATION"], let answer = env["MEOW_UITEST_ANSWER"] else {
            throw XCTSkip("set MEOW_UITEST_NOTIFICATION and MEOW_UITEST_ANSWER to run against a local server")
        }
        let app = XCUIApplication()
        app.launchArguments = ["-meowOpen", id, "-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()

        // Identifiers, not labels: the simulator may run in any language.
        let field = app.descendants(matching: .any).matching(identifier: "reply.field").firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 10), "reply field not shown")
        let before = XCTAttachment(screenshot: app.screenshot())
        before.name = "question-text-reply"
        before.lifetime = .keepAlways
        add(before)
        field.tap()
        field.typeText(answer)
        app.buttons["reply.send"].tap()

        // The panel switches from the reply field to the answered state.
        XCTAssertTrue(app.staticTexts[answer].waitForExistence(timeout: 10), "answer not shown as sent")
        XCTAssertFalse(field.exists)
        let after = XCTAttachment(screenshot: app.screenshot())
        after.name = "question-answered"
        after.lifetime = .keepAlways
        add(after)
    }
}

final class OnboardingRegressionUITests: XCTestCase {
    private func reveal(_ element: XCUIElement, in app: XCUIApplication) {
        // SwiftUI may report clipped content as hittable behind the fixed footer.
        let visibleArea = CGRect(x: app.frame.minX, y: app.frame.minY + 100,
                                 width: app.frame.width, height: app.frame.height - 240)
        for _ in 0..<10 {
            if element.exists && element.isHittable && visibleArea.contains(CGPoint(x: element.frame.midX, y: element.frame.midY)) { return }
            let start = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.65))
            let end = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.4))
            start.press(forDuration: 0.1, thenDragTo: end)
        }
        XCTFail("control did not become visible above the footer")
    }

    private func replaceAddress(_ field: XCUIElement, with value: String) {
        field.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap()
        field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: (field.value as? String)?.count ?? 0))
        field.typeText(value)
    }

    private func send(title: String, server: String, key: String) throws {
        var request = URLRequest(url: try XCTUnwrap(URL(string: server + "/v1/notifications")))
        request.httpMethod = "POST"
        request.setValue("Bearer \(key)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: ["title": title, "body": "UI test"])
        let done = expectation(description: "sent")
        var status = 0
        URLSession.shared.dataTask(with: request) { _, response, _ in
            status = (response as? HTTPURLResponse)?.statusCode ?? 0
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 10)
        XCTAssertTrue((200..<300).contains(status), "server answered \(status)")
    }

    func testInvalidAndUnreachableServerShowSpecificErrors() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()
        let address = app.textFields["server.address"]
        guard address.waitForExistence(timeout: 10) else {
            throw XCTSkip("this simulator is already registered; erase it to test first launch")
        }
        XCTAssertEqual(address.value as? String, "https://api.meow.blue")
        replaceAddress(address, with: "localhost\n")
        XCTAssertTrue(app.staticTexts["Enter a full address such as https://meow.example.com."].waitForExistence(timeout: 5))
        // Relaunch restores the official suggestion; replace it before the next check.
        app.terminate()
        app.launch()
        XCTAssertTrue(address.waitForExistence(timeout: 10))
        replaceAddress(address, with: "http://127.0.0.1:1\n")
        XCTAssertTrue(app.staticTexts["Can't reach 127.0.0.1. Check the address and that the server is running."].waitForExistence(timeout: 15))
        XCTAssertFalse(app.buttons["Not now"].exists, "an unreachable server must not register")
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "onboarding-server-error"
        shot.lifetime = .keepAlways
        add(shot)
    }

    func testSkipPermissionAndFinishLoadsInbox() throws {
        guard let server = ProcessInfo.processInfo.environment["MEOW_UITEST_SERVER"] else {
            throw XCTSkip("set MEOW_UITEST_SERVER to a disposable local server")
        }
        let app = XCUIApplication()
        app.launchArguments = ["-meowServer", server, "-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()
        let start = app.buttons["Get started"]
        XCTAssertTrue(start.waitForExistence(timeout: 10))
        let welcome = XCTAttachment(screenshot: app.screenshot())
        welcome.name = "onboarding-welcome"
        welcome.lifetime = .keepAlways
        add(welcome)
        start.tap()
        let skip = app.buttons["Not now"]
        XCTAssertTrue(skip.waitForExistence(timeout: 10))
        let permissions = XCTAttachment(screenshot: app.screenshot())
        permissions.name = "onboarding-notifications"
        permissions.lifetime = .keepAlways
        add(permissions)
        skip.tap()
        let next = app.buttons["Next"]
        XCTAssertTrue(next.waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["Copy Device Key"].exists)
        XCTAssertTrue(app.buttons["Share setup command"].exists)
        let connect = XCTAttachment(screenshot: app.screenshot())
        connect.name = "onboarding-connect"
        connect.lifetime = .keepAlways
        add(connect)
        let showKey = app.buttons["Show Device Key"]
        reveal(showKey, in: app)
        showKey.tap()
        XCTAssertTrue(app.buttons["Hide Device Key"].waitForExistence(timeout: 5))
        let key = app.staticTexts["onboarding.deviceKey"].label
        XCTAssertTrue(key.hasPrefix("meow_"))
        XCTAssertFalse(key.contains("…"), "the real API must receive the full key")
        app.buttons["Hide Device Key"].tap()
        next.tap()
        let waitingText = app.staticTexts["Waiting for a test notification from your computer…"]
        XCTAssertTrue(waitingText.waitForExistence(timeout: 10))
        let waiting = XCTAttachment(screenshot: app.screenshot())
        waiting.name = "onboarding-waiting"
        waiting.lifetime = .keepAlways
        add(waiting)

        // A test sent from the phone proves the server works, not the computer.
        app.buttons["Send a test from this iPhone"].tap()
        sleep(5) // more than two polls
        XCTAssertTrue(waitingText.exists, "a test from this iPhone must not count as the computer connecting")

        // Send as the computer would, with the Device Key.
        try send(title: "From your computer", server: server, key: key)
        let success = app.staticTexts["Connected — received “From your computer”"]
        XCTAssertTrue(success.waitForExistence(timeout: 10), "a new notification ends the wait")
        let connected = XCTAttachment(screenshot: app.screenshot())
        connected.name = "onboarding-connected"
        connected.lifetime = .keepAlways
        add(connected)
        // Relaunch before finishing. The same received message must count;
        // a new baseline must not silently swallow it and wait for another push.
        app.terminate()
        app.launch()
        XCTAssertTrue(app.buttons["Not now"].waitForExistence(timeout: 10))
        app.buttons["Not now"].tap()
        XCTAssertTrue(app.buttons["Next"].waitForExistence(timeout: 10))
        app.buttons["Next"].tap()
        XCTAssertTrue(success.waitForExistence(timeout: 10), "receipt survives relaunch without another push")
        app.buttons["Done"].tap()
        XCTAssertTrue(app.staticTexts["From your computer"].waitForExistence(timeout: 10),
                      "finishing onboarding must load the inbox without a scene phase transition")
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "onboarding-inbox-loaded"
        shot.lifetime = .keepAlways
        add(shot)
    }
}


final class ScreenFlowUITests: XCTestCase {
    private func open(_ id: String? = nil) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        if let id { app.launchArguments += ["-meowOpen", id] }
        app.launch()
        return app
    }

    private func capture(_ name: String, app: XCUIApplication) {
        let screenshot = XCTAttachment(screenshot: app.screenshot())
        screenshot.name = name
        screenshot.lifetime = .keepAlways
        add(screenshot)
    }

    func testInboxAndSettings() throws {
        guard ProcessInfo.processInfo.environment["MEOW_UITEST_NOTIFICATION"] != nil else {
            throw XCTSkip("run the verification script to seed a local inbox")
        }
        let app = open()
        XCTAssertTrue(app.staticTexts["UI test: build completed"].waitForExistence(timeout: 10))
        capture("inbox", app: app)
        app.tabBars.buttons["Settings"].tap()
        capture("settings", app: app)
        let copyKey = app.buttons["Copy Device Key"]
        for _ in 0..<8 {
            if copyKey.isHittable { break }
            app.swipeUp()
        }
        XCTAssertTrue(copyKey.isHittable, "the key action must remain reachable with large text")
    }

    func testRejectApprovalFromDetail() throws {
        guard let id = ProcessInfo.processInfo.environment["MEOW_UITEST_APPROVAL_NOTIFICATION"] else {
            throw XCTSkip("run the verification script to seed an approval")
        }
        let app = open(id)
        XCTAssertTrue(app.buttons["Approve"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["Reject"].exists)
        capture("approval-detail", app: app)
        app.buttons["Reject"].tap()
        XCTAssertTrue(app.staticTexts["Rejected"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["Approve"].exists)
        capture("approval-rejected", app: app)
    }

    func testCustomChoiceFromDetail() throws {
        guard let id = ProcessInfo.processInfo.environment["MEOW_UITEST_CHOICE_NOTIFICATION"] else {
            throw XCTSkip("run the verification script to seed a choice question")
        }
        let app = open(id)
        XCTAssertTrue(app.buttons["sqlite"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["Approve"].exists)
        capture("custom-choice-detail", app: app)
        app.buttons["sqlite"].tap()
        XCTAssertTrue(app.staticTexts["You answered"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["sqlite"].exists)
    }
}


final class OfflineReplyUITests: XCTestCase {
    func testKeepWaitingPreservesSavedReply() throws {
        guard let id = ProcessInfo.processInfo.environment["MEOW_UITEST_OFFLINE_NOTIFICATION"] else {
            throw XCTSkip("run the verification script with its offline proxy")
        }
        let app = XCUIApplication()
        app.launchArguments = ["-meowOpen", id, "-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()
        let field = app.descendants(matching: .any).matching(identifier: "reply.field").firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        field.tap()
        field.typeText("Keep this queued answer.")
        app.buttons["reply.send"].tap()
        XCTAssertTrue(app.staticTexts["Your answer is saved and will be sent when the connection is back."].waitForExistence(timeout: 10))
        let pending = XCTAttachment(screenshot: app.screenshot())
        pending.name = "offline-reply-saved"
        pending.lifetime = .keepAlways
        add(pending)
        app.navigationBars.buttons.element(boundBy: 0).tap()
        app.tabBars.buttons["Settings"].tap()
        let changeServer = app.buttons["Change server…"]
        for _ in 0..<4 {
            if changeServer.exists { break }
            app.swipeUp()
        }
        XCTAssertTrue(changeServer.exists)
        changeServer.tap()
        let address = app.textFields.firstMatch
        XCTAssertTrue(address.waitForExistence(timeout: 5))
        address.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5)).tap()
        let length = (address.value as? String)?.count ?? 64
        address.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: length))
        address.typeText("http://127.0.0.1:1")
        app.buttons["Save"].tap()
        let confirmation = app.alerts["Discard saved replies?"]
        XCTAssertTrue(confirmation.waitForExistence(timeout: 5))
        XCTAssertTrue(confirmation.buttons["Discard and switch"].isHittable)
        XCTAssertTrue(confirmation.buttons["Cancel"].isHittable)
        let window = app.windows.firstMatch.frame
        XCTAssertEqual(confirmation.frame.midX, window.midX, accuracy: 3)
        XCTAssertEqual(confirmation.frame.midY, window.midY, accuracy: window.height * 0.12)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "already processed")).firstMatch.exists)
        let choices = XCTAttachment(screenshot: app.screenshot())
        choices.name = "server-switch-choices"
        choices.lifetime = .keepAlways
        add(choices)
        confirmation.buttons["Cancel"].tap()
        XCTAssertTrue(address.waitForExistence(timeout: 5))
        app.buttons["Cancel"].tap()
        XCTAssertTrue(changeServer.waitForExistence(timeout: 5))
    }
}


final class DiscardReplyUITests: XCTestCase {
    func testConfirmedDiscardSwitchesServer() throws {
        guard let id = ProcessInfo.processInfo.environment["MEOW_UITEST_OFFLINE_NOTIFICATION"] else {
            throw XCTSkip("run the verification script with its offline proxy")
        }
        let app = XCUIApplication()
        app.launchArguments = ["-meowOpen", id, "-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()
        let field = app.descendants(matching: .any).matching(identifier: "reply.field").firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        field.tap()
        field.typeText("Discard this queued answer.")
        app.buttons["reply.send"].tap()
        XCTAssertTrue(app.staticTexts["Your answer is saved and will be sent when the connection is back."].waitForExistence(timeout: 10))
        let pending = XCTAttachment(screenshot: app.screenshot())
        pending.name = "discard-reply-saved"
        pending.lifetime = .keepAlways
        add(pending)
        app.navigationBars.buttons.element(boundBy: 0).tap()
        app.tabBars.buttons["Settings"].tap()
        let changeServer = app.buttons["Change server…"]
        for _ in 0..<4 {
            if changeServer.exists { break }
            app.swipeUp()
        }
        XCTAssertTrue(changeServer.exists)
        changeServer.tap()
        let address = app.textFields.firstMatch
        XCTAssertTrue(address.waitForExistence(timeout: 5))
        address.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5)).tap()
        let length = (address.value as? String)?.count ?? 64
        address.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: length))
        address.typeText("http://127.0.0.1:1")
        app.buttons["Save"].tap()
        let confirmation = app.alerts["Discard saved replies?"]
        XCTAssertTrue(confirmation.waitForExistence(timeout: 5))
        XCTAssertTrue(confirmation.buttons["Discard and switch"].isHittable)
        XCTAssertTrue(confirmation.buttons["Cancel"].isHittable)
        let window = app.windows.firstMatch.frame
        XCTAssertEqual(confirmation.frame.midX, window.midX, accuracy: 3)
        XCTAssertEqual(confirmation.frame.midY, window.midY, accuracy: window.height * 0.12)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "already processed")).firstMatch.exists)
        let choices = XCTAttachment(screenshot: app.screenshot())
        choices.name = "server-switch-choices"
        choices.lifetime = .keepAlways
        add(choices)
        confirmation.buttons["Discard and switch"].tap()
        XCTAssertTrue(app.buttons["Get started"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.tabBars.buttons["Settings"].exists)
        app.terminate()
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()
        XCTAssertTrue(app.buttons["Get started"].waitForExistence(timeout: 10))
        let switched = XCTAttachment(screenshot: app.screenshot())
        switched.name = "server-switched-after-discard"
        switched.lifetime = .keepAlways
        add(switched)
    }
}

/// Presentation regressions: verify native alerts and real tappable geometry,
/// in addition to checking that business actions still complete.
final class DesignRegressionUITests: XCTestCase {
    private func openSettings() throws -> XCUIApplication {
        guard ProcessInfo.processInfo.environment["MEOW_UITEST_NOTIFICATION"] != nil else {
            throw XCTSkip("run the verification script with a seeded local inbox")
        }
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()
        app.tabBars.buttons["Settings"].tap()
        return app
    }

    private func reveal(_ button: XCUIElement, in app: XCUIApplication) {
        for _ in 0..<8 {
            if button.isHittable { return }
            app.swipeUp()
        }
        XCTAssertTrue(button.isHittable)
    }

    private func assertCentered(_ alert: XCUIElement, in app: XCUIApplication) {
        XCTAssertTrue(alert.waitForExistence(timeout: 5), "confirmation must use a system alert")
        let window = app.windows.firstMatch.frame
        XCTAssertEqual(alert.frame.midX, window.midX, accuracy: 3)
        XCTAssertEqual(alert.frame.midY, window.midY, accuracy: window.height * 0.12)
        XCTAssertTrue(window.contains(alert.frame), "alert must remain inside the screen")
    }

    private func capture(_ name: String, in app: XCUIApplication) {
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = name
        shot.lifetime = .keepAlways
        add(shot)
    }

    func testKeyRotationUsesCenteredCancellableAlert() throws {
        let app = try openSettings()
        let rotate = app.buttons["Create new Device Key…"]
        reveal(rotate, in: app)
        rotate.tap()
        let alert = app.alerts["Create a new Device Key?"]
        assertCentered(alert, in: app)
        XCTAssertTrue(alert.buttons["Cancel"].isHittable)
        XCTAssertTrue(alert.buttons["Create new key"].isHittable)
        capture("key-rotation-confirmation", in: app)
        alert.buttons["Cancel"].tap()
        XCTAssertFalse(alert.exists)
        XCTAssertTrue(rotate.isHittable)
    }

    func testServerEditorUsesCenteredCancellableAlert() throws {
        let app = try openSettings()
        let change = app.buttons["Change server…"]
        reveal(change, in: app)
        change.tap()
        let address = app.textFields.firstMatch
        XCTAssertTrue(address.waitForExistence(timeout: 5))
        capture("server-editor", in: app)
        address.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5)).tap()
        let length = (address.value as? String)?.count ?? 64
        address.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: length))
        address.typeText("http://127.0.0.1:1")
        app.buttons["Save"].tap()
        let alert = app.alerts["Switch servers?"]
        assertCentered(alert, in: app)
        XCTAssertFalse(app.keyboards.firstMatch.exists)
        capture("server-switch-confirmation", in: app)
        alert.buttons["Cancel"].tap()
        XCTAssertTrue(address.isHittable)
        app.buttons["Cancel"].tap()
    }
}

extension DesignRegressionUITests {
    func testFilterTargetsAndEmptyStateStayUsable() throws {
        let app = try openSettings()
        app.tabBars.buttons["Inbox"].tap()
        let menu = app.buttons["inbox.browseMenu"]
        XCTAssertTrue(menu.waitForExistence(timeout: 5))
        XCTAssertGreaterThanOrEqual(menu.frame.height, 44)
        menu.tap()
        for name in ["all", "unread", "pending"] {
            let filter = app.buttons["filter.\(name)"]
            XCTAssertTrue(filter.waitForExistence(timeout: 5))
            XCTAssertGreaterThanOrEqual(filter.frame.height, 44)
        }
        app.buttons["Mark all as read"].tap()
        menu.tap()
        app.buttons["filter.unread"].tap()
        XCTAssertTrue(app.staticTexts["All caught up"].waitForExistence(timeout: 10))
        XCTAssertTrue(menu.isHittable, "the empty view must not cover the filter menu")
        capture("inbox-empty-unread", in: app)
        menu.tap()
        app.buttons["filter.all"].tap()
        XCTAssertTrue(app.staticTexts["UI test: build completed"].waitForExistence(timeout: 10))
    }

    func testReplyComposerKeepsDraftAndDismissesKeyboard() throws {
        guard let id = ProcessInfo.processInfo.environment["MEOW_UITEST_COMPOSER_NOTIFICATION"] else {
            throw XCTSkip("run the verification script with a pending question")
        }
        let app = XCUIApplication()
        app.launchArguments = ["-meowOpen", id, "-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()
        let field = app.descendants(matching: .any).matching(identifier: "reply.field").firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        reveal(field, in: app)
        XCTAssertFalse(app.tabBars.firstMatch.exists, "detail should focus on reading and answering")
        let send = app.buttons["reply.send"]
        XCTAssertGreaterThanOrEqual(send.frame.width, 44)
        XCTAssertGreaterThanOrEqual(send.frame.height, 44)
        field.tap()
        field.typeText("Review the updated interface.\nPreserve the typed draft.")
        XCTAssertTrue(send.isHittable, "typing a long reply must not hide the send action behind the keyboard")
        XCTAssertLessThanOrEqual(send.frame.maxY, app.keyboards.firstMatch.frame.minY + 1)
        capture("reply-keyboard", in: app)
        app.buttons["Done"].tap()
        let hidden = XCTNSPredicateExpectation(predicate: NSPredicate(format: "exists == false"), object: app.keyboards.firstMatch)
        XCTAssertEqual(XCTWaiter.wait(for: [hidden], timeout: 5), .completed)
        XCTAssertTrue((field.value as? String)?.contains("Preserve the typed draft.") == true)
        capture("reply-draft", in: app)
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.tabBars.buttons["Inbox"].waitForExistence(timeout: 5))
    }
}

extension DesignRegressionUITests {
    func testChineseConfirmationUsesLocalizedCenteredAlert() throws {
        guard ProcessInfo.processInfo.environment["MEOW_UITEST_NOTIFICATION"] != nil else {
            throw XCTSkip("run the verification script")
        }
        let app = XCUIApplication()
        app.launchArguments = ["-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"]
        app.launch()
        app.tabBars.buttons["设置"].tap()
        let change = app.buttons["更换服务器…"]
        reveal(change, in: app)
        change.tap()
        let address = app.textFields.firstMatch
        XCTAssertTrue(address.waitForExistence(timeout: 5))
        address.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5)).tap()
        let length = (address.value as? String)?.count ?? 64
        address.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: length))
        address.typeText("http://127.0.0.1:1")
        app.buttons["保存"].tap()
        let alert = app.alerts["要更换服务器吗？"]
        assertCentered(alert, in: app)
        XCTAssertTrue(alert.buttons["切换服务器"].isHittable)
        capture("server-switch-chinese", in: app)
        alert.buttons["取消"].tap()
        app.buttons["取消"].tap()
    }

    func testInvalidAddressShowsInlineValidation() throws {
        let app = try openSettings()
        let change = app.buttons["Change server…"]
        reveal(change, in: app)
        change.tap()
        let address = app.textFields.firstMatch
        XCTAssertTrue(address.waitForExistence(timeout: 5))
        address.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5)).tap()
        let length = (address.value as? String)?.count ?? 64
        address.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: length))
        address.typeText("localhost")
        app.buttons["Save"].tap()
        let validation = app.staticTexts["Enter a full address such as https://meow.example.com."]
        XCTAssertTrue(validation.waitForExistence(timeout: 5))
        XCTAssertTrue(validation.isHittable, "validation must be visible instead of hidden behind the keyboard")
        XCTAssertFalse(app.alerts.firstMatch.exists)
        XCTAssertTrue(address.isHittable)
        capture("server-invalid-address", in: app)
        app.buttons["Cancel"].tap()
    }
}
