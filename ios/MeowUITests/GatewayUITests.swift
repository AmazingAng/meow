import XCTest

final class GatewayUITests: XCTestCase {
    func testAutomaticEnrollmentAndServerChange() throws {
        let env = ProcessInfo.processInfo.environment
        guard let server = env["MEOW_GATEWAY_UITEST_SERVER"] else {
            throw XCTSkip("requires the disposable gateway verification harness")
        }
        let app = XCUIApplication()
        app.launchArguments = ["-meowServer", server, "-meowAutoRegister", "YES", "-meowAPNsToken", String(repeating: "a", count: 64),
                               "-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launch()
        XCTAssertTrue(app.tabBars.buttons["Settings"].waitForExistence(timeout: 15))
        XCTAssertFalse(app.secureTextFields["gateway.invitation"].exists)
        app.tabBars.buttons["Settings"].tap()
        app.buttons["settings.advanced"].tap()
        let retry = app.buttons["Retry push registration"]
        XCTAssertTrue(retry.waitForExistence(timeout: 10))
        // Leave time for the simulator's own setup notification to disappear.
        Thread.sleep(forTimeInterval: 8)
        let connected = XCTAttachment(screenshot: app.screenshot())
        connected.name = "gateway-auto-connected"
        connected.lifetime = .keepAlways
        add(connected)
        retry.tap()
        XCTAssertTrue(app.navigationBars["Advanced settings"].exists, "retry must not open a setup step")
        XCTAssertFalse(app.secureTextFields["gateway.invitation"].exists)
        app.buttons["Change server…"].tap()
        let address = app.textFields["server.address"]
        XCTAssertTrue(address.waitForExistence(timeout: 5))
        address.tap()
        if let current = address.value as? String { address.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: current.count)) }
        address.typeText(server + "/other")
        app.buttons["Save"].tap()
        let change = app.alerts.buttons["Switch server"]
        XCTAssertTrue(change.waitForExistence(timeout: 5))
        change.tap()
        XCTAssertTrue(app.buttons["Get started"].waitForExistence(timeout: 10))
        let switched = XCTAttachment(screenshot: app.screenshot())
        switched.name = "gateway-server-changed"
        switched.lifetime = .keepAlways
        add(switched)
    }
}
