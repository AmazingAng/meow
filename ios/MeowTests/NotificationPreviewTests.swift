import XCTest
import UserNotifications
@testable import Meow

final class NotificationPreviewTests: XCTestCase {
    struct Fixture: Decodable { let name: String; let body: String; let expected: String }
    func testSharedPreviewFixtures() throws {
        let url = try XCTUnwrap(Bundle(for: Self.self).url(forResource: "notification-preview", withExtension: "json"))
        for item in try JSONDecoder().decode([Fixture].self, from: Data(contentsOf: url)) {
            XCTAssertEqual(NotificationContent.preview(item.body), item.expected, item.name)
        }
    }
}

extension GatewayContentTests {
    func testRelayPreviewDoesNotDiscardStructuredBody() throws {
        for body in ["{\"suggestions\":[]}", "```sh\necho hello\n```"] {
            var n = notification()
            n.body = body
            let content = try XCTUnwrap(NotificationContent.hydrate(n, original: wake(), identity: identity))
            XCTAssertEqual(content.title, n.title)
            XCTAssertEqual(content.body, "")
            XCTAssertEqual(n.body, body)
        }
    }
}

extension GatewayContentTests {
    func testRelayBannerRendersMarkdownBeforeTruncation() throws {
        var n = notification()
        n.body = "**Bark Server 不持久保存通知正文和消息历史。** 看 [源码](https://example.com)。"
        let hydrated = try XCTUnwrap(NotificationContent.hydrate(n, original: wake(), identity: identity))
        let displayed = NotificationContent.forDisplay(hydrated, identity: identity)
        XCTAssertEqual(displayed.body, "Bark Server 不持久保存通知正文和消息历史。 看 源码。")
        XCTAssertTrue(n.body.contains("**"))
    }

    func testLegacyDirectBannerAndTimeoutStripMarkdownOnce() {
        let direct = UNMutableNotificationContent()
        direct.body = "New result\n**构建完成**，使用 `meow doctor`。"
        direct.userInfo = ["meow": ["id": "ntf_direct", "device_id": "dev_1"]]
        let displayed = NotificationContent.forDisplay(direct, identity: identity)
        XCTAssertEqual(displayed.body, "New result\n构建完成，使用 meow doctor。")
        XCTAssertEqual(direct.body, "New result\n**构建完成**，使用 `meow doctor`。")
        var delivered: UNNotificationContent?
        let delivery = NotificationDelivery(original: direct, currentIdentity: { self.identity },
            fetch: { _, _ in throw URLError(.timedOut) }, report: { _, _ in }, handler: { delivered = $0 })
        delivery.expire()
        XCTAssertEqual(delivered?.body, displayed.body)
        XCTAssertEqual(NotificationContent.forDisplay(displayed, identity: identity).body, displayed.body)
    }

    func testAlreadyPlainDirectAndRelayBannersPreserveLiteralMarkdownCharacters() throws {
        let direct = UNMutableNotificationContent()
        direct.body = "Use **literal** and snake_case"
        direct.userInfo = ["meow": ["id": "ntf_direct", "device_id": "dev_1", "preview_format": "plain"]]
        XCTAssertEqual(NotificationContent.forDisplay(direct, identity: identity).body, direct.body)
        var n = notification()
        n.body = "Use `**literal**` and snake_case"
        let hydrated = try XCTUnwrap(NotificationContent.hydrate(n, original: wake(), identity: identity))
        XCTAssertEqual(NotificationContent.forDisplay(hydrated, identity: identity).body, "Use **literal** and snake_case")
    }
}
