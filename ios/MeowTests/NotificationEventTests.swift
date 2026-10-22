import XCTest
@testable import Meow

final class NotificationEventTests: XCTestCase {
    private func notification(title: String, event: String?) throws -> MeowNotification {
        var object: [String: Any] = [
            "id": "ntf_event", "type": "info", "title": title, "body": "**完整正文** 😺", "source": "Codex",
            "priority": "low", "push_status": "accepted", "created_at": "2026-10-22T00:00:00Z",
            "context": ["workspace": "meow", "session": "部署审查", "session_id": "s1"]
        ]
        if let event { object["event"] = event }
        return try MeowJSON.decoder.decode(MeowNotification.self, from: JSONSerialization.data(withJSONObject: object))
    }

    func testCategoryUsesEventAndAlwaysPreservesCustomTitle() throws {
        for (event, symbol) in [("result", "bubble.left"), ("attention", "checklist"), ("failure", "exclamationmark.triangle")] {
            for title in ["Build 19 is ready", "自定义标题", "Le déploiement est prêt"] {
                let n = try notification(title: title, event: event)
                XCTAssertEqual(n.inboxCategory?.symbol, symbol)
                XCTAssertEqual(n.compactHeadline, title)
                XCTAssertEqual(n.compactMetadata, "部署审查 · Codex · meow")
                XCTAssertEqual(n.priority, "low")
                XCTAssertEqual(n.type, .info)
            }
        }
    }

    func testStandardLookingTitleWithoutEventIsGeneric() throws {
        for title in ["New result", "Needs your attention", "Run failed", "有新结果", "需要你确认", "运行出错"] {
            let n = try notification(title: title, event: nil)
            XCTAssertNil(n.inboxCategory)
            XCTAssertEqual(n.compactHeadline, title)
        }
    }

    func testEventSurvivesEncodingAndLocalSQLite() async throws {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
        for event in ["result", "attention", "failure"] {
            let n = try notification(title: "自定义标题", event: event)
            try await store.save([n], scope: "event-test")
            let saved = try await store.load(scope: "event-test")
            let item = try XCTUnwrap(saved?.first)
            let object = try XCTUnwrap(JSONSerialization.jsonObject(with: JSONEncoder().encode(item)) as? [String: Any])
            XCTAssertEqual(object["event"] as? String, event)
            XCTAssertEqual(item, n)
            XCTAssertNotNil(item.inboxCategory)
        }
    }
}
