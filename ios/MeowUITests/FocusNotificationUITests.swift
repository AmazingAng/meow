import XCTest

final class FocusNotificationUITests: XCTestCase {
    func testFocusSettingsInEnglishAndChinese() throws {
        guard let server = ProcessInfo.processInfo.environment["MEOW_FOCUS_UITEST_SERVER"] else {
            throw XCTSkip("set MEOW_FOCUS_UITEST_SERVER to a disposable local server")
        }
        for (language, settings, title, button) in [
            ("en", "Settings", "During Focus", "Send a time-sensitive test"),
            ("zh-Hans", "设置", "免打扰提醒", "发送时效性测试通知"),
        ] {
            let app = XCUIApplication()
            app.launchArguments = ["-meowServer", server, "-meowAutoRegister", "YES",
                                   "-AppleLanguages", "(\(language))", "-AppleLocale", language == "en" ? "en_US" : "zh_CN"]
            app.launch()
            XCTAssertTrue(app.tabBars.buttons[settings].waitForExistence(timeout: 15))
            app.tabBars.buttons[settings].tap()
            app.buttons["settings.notifications"].tap()
            let preview = app.buttons["notifications.previewSound"]
            XCTAssertTrue(preview.waitForExistence(timeout: 5))
            for _ in 0..<3 where !preview.isHittable { app.swipeUp() }
            preview.tap()
            XCTAssertTrue(app.navigationBars[language == "en" ? "Notifications" : "通知"].exists)
            XCTAssertEqual(app.alerts.count, 0, "preview must load the bundled sound")
            let soundScreenshot = XCTAttachment(screenshot: app.screenshot())
            soundScreenshot.name = "meow-sound-settings-\(language)"
            soundScreenshot.lifetime = .keepAlways
            add(soundScreenshot)
            for _ in 0..<4 where !app.buttons[button].isHittable { app.swipeUp() }
            XCTAssertTrue(app.staticTexts[title].exists)
            XCTAssertTrue(app.buttons[button].exists)
            // Include the Focus instructions below the button in the capture.
            app.swipeUp()
            let screenshot = XCTAttachment(screenshot: app.screenshot())
            screenshot.name = "focus-settings-\(language)"
            screenshot.lifetime = .keepAlways
            add(screenshot)
            app.terminate()
        }
    }
}
