import XCTest

final class FocusNotificationUITests: XCTestCase {
    func testNotificationSettingsInEnglishAndChinese() throws {
        guard let server = ProcessInfo.processInfo.environment["MEOW_FOCUS_UITEST_SERVER"] else {
            throw XCTSkip("set MEOW_FOCUS_UITEST_SERVER to a disposable local server")
        }
        for (language, settings) in [("en", "Settings"), ("zh-Hans", "设置")] {
            let app = XCUIApplication()
            app.launchArguments = ["-meowServer", server, "-meowAutoRegister", "YES",
                                   "-AppleLanguages", "(\(language))", "-AppleLocale", language == "en" ? "en_US" : "zh_CN"]
            app.launch()
            XCTAssertTrue(app.tabBars.buttons[settings].waitForExistence(timeout: 15))
            app.tabBars.buttons[settings].tap()
            XCTAssertTrue(app.buttons["settings.notifications"].exists)
            XCTAssertTrue(app.buttons["notifications.sound"].exists)
            XCTAssertFalse(app.buttons["notifications.previewSound"].exists)
            let screenshot = XCTAttachment(screenshot: app.screenshot())
            screenshot.name = "notification-settings-\(language)"
            screenshot.lifetime = .keepAlways
            add(screenshot)
            app.terminate()
        }
    }
}
