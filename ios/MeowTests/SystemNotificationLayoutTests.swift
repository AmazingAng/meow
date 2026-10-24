import XCTest
import UserNotifications
@testable import Meow

extension GatewayContentTests {
    func testSystemNotificationLayoutMatchesDirectAPNsFixtures() throws {
        struct Layout: Decodable {
            var name, title, source, body, expectedTitle, expectedSubtitle, expectedBody: String
            var event: NotificationEvent?
            var context: NotificationOrigin?
            var localizedSessionFallback: Bool?
        }
        let url = try XCTUnwrap(Bundle(for: Self.self).url(forResource: "notification-layout", withExtension: "json"))
        for fixture in try MeowJSON.decoder.decode([Layout].self, from: Data(contentsOf: url)) {
            var n = notification()
            n.title = fixture.title; n.source = fixture.source; n.body = fixture.body
            n.context = fixture.context; n.event = fixture.event
            let original = wake()
            original.subtitle = "Untrusted project · agent"
            let hydrated = try XCTUnwrap(NotificationContent.hydrate(n, original: original, identity: identity))
            let content = NotificationContent.forDisplay(hydrated, identity: identity)
            let title = fixture.localizedSessionFallback == true
                ? String(localized: "Session") + fixture.expectedTitle.dropFirst("Session".count)
                : fixture.expectedTitle
            XCTAssertEqual(content.title, title, fixture.name)
            XCTAssertEqual(content.subtitle, fixture.expectedSubtitle, fixture.name)
            XCTAssertEqual(content.body, fixture.expectedBody, fixture.name)
            XCTAssertEqual(n.title, fixture.title, "banner layout must not rewrite the inbox title")
            XCTAssertEqual(n.body, fixture.body, "banner layout must not rewrite the inbox body")
        }
    }

    func testSystemNotificationFallbackClearsUnverifiedSubtitle() throws {
        let original = wake()
        original.subtitle = "Private project · Wrong agent"
        XCTAssertEqual(NotificationContent.forDisplay(original, identity: identity).subtitle, "")
        var n = notification()
        n.context = NotificationOrigin(workspace: "meow", session: "Review")
        let verified = try XCTUnwrap(NotificationContent.hydrate(n, original: original, identity: identity))
        var switched = identity; switched.bindingID = "new-binding"
        let stale = NotificationContent.forDisplay(verified, identity: switched)
        XCTAssertEqual(stale.title, "Meow")
        XCTAssertEqual(stale.subtitle, "")
        XCTAssertFalse(stale.body.contains("Review"))
    }
}
