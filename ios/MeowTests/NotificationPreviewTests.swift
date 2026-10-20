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
            XCTAssertEqual(content.body, n.title)
            XCTAssertEqual(n.body, body)
        }
    }
}
