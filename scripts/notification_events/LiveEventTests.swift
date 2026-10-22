import XCTest
@testable import Meow

final class LiveEventTests: XCTestCase {
    func testRealServerRecordsSurviveIndependentLocalHistory() async throws {
        let file = try XCTUnwrap(Bundle.module.url(forResource: "live-notifications", withExtension: "json"))
        let records = try MeowJSON.decoder.decode([MeowNotification].self, from: Data(contentsOf: file))
        XCTAssertEqual(records.count, 4)
        XCTAssertEqual(Set(records.compactMap(\.event)), Set(NotificationEvent.allCases))
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
        try await store.save(records, scope: "isolated-live")
        let loaded = try await store.load(scope: "isolated-live")
        let saved = try XCTUnwrap(loaded)
        XCTAssertEqual(Set(saved), Set(records))
        for n in saved {
            if let event = n.event {
                XCTAssertEqual(n.inboxCategory, event)
                XCTAssertEqual(n.compactHeadline, "部署审查")
                XCTAssertEqual(n.title, "自定义标题")
                XCTAssertEqual(n.compactMetadata, "meow · CI")
                XCTAssertEqual(n.priority, "low")
            } else {
                XCTAssertEqual(n.title, "New result")
                XCTAssertNil(n.inboxCategory)
            }
        }
        // Reopen the local database independently after the rehearsal server stopped.
        let reopened = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
        let retained = try await reopened.load(scope: "isolated-live")
        XCTAssertEqual(Set(try XCTUnwrap(retained)), Set(records))
    }
}
