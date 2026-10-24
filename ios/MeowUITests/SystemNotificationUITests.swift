import XCTest

/// Actual SpringBoard banner, using only a disposable simctl APNs fixture.
final class SystemNotificationUITests: XCTestCase {
    func testSessionProjectAndBodyInSystemBanner() throws {
        continueAfterFailure = false
        guard let server = ProcessInfo.processInfo.environment["MEOW_SYSTEM_BANNER_SERVER"] else {
            throw XCTSkip("run ios/scripts/verify_system_notification_banner.py")
        }
        let app = XCUIApplication()
        app.launchArguments = ["-meowServer", server, "-meowAutoRegister", "YES",
                               "-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()
        XCTAssertTrue(app.tabBars.buttons["Settings"].waitForExistence(timeout: 10))
        app.tabBars.buttons["Settings"].tap()
        let permission = app.buttons["Allow notifications"]
        for _ in 0..<6 where !permission.isHittable { app.swipeUp() }
        XCTAssertTrue(permission.isHittable); permission.tap()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        // The app language does not change SpringBoard's simulator language.
        let allow = springboard.alerts.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许"])).firstMatch
        XCTAssertTrue(allow.waitForExistence(timeout: 10)); allow.tap()
        XCTAssertTrue(app.buttons["Open notification settings"].waitForExistence(timeout: 10))
        XCUIDevice.shared.press(.home)
        let pushed = expectation(description: "simulated APNs sent")
        URLSession.shared.dataTask(with: URL(string: server + "/fixture/system-push")!) { data, response, error in
            XCTAssertNil(error)
            XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
            pushed.fulfill()
        }.resume()
        wait(for: [pushed], timeout: 20)
        let title = springboard.staticTexts["修复通知布局"]
        XCTAssertTrue(title.waitForExistence(timeout: 20))
        let subtitle = springboard.staticTexts["meow · Codex"]
        let body = springboard.staticTexts["修改完成，使用 meow doctor。"]
        XCTAssertTrue(subtitle.exists)
        XCTAssertTrue(body.exists)
        XCTAssertLessThan(title.frame.midY, subtitle.frame.midY)
        XCTAssertLessThan(subtitle.frame.midY, body.frame.midY)
        XCTAssertFalse(springboard.staticTexts["New result · meow"].exists)
        let screenshot = XCTAttachment(screenshot: springboard.screenshot())
        screenshot.name = "system-notification-session-project-body"
        screenshot.lifetime = .keepAlways
        add(screenshot)
    }
}
