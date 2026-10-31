import XCTest
import SQLite3
@testable import Meow

private func syncItem(_ id: String, read: Bool = false) -> MeowNotification {
    MeowNotification(id: id, type: .info, title: "同步测试 😺", body: "Original body", source: "Test", priority: "normal",
                     pushStatus: "accepted", createdAt: Date(timeIntervalSince1970: 1_790_000_000), readAt: read ? .now : nil)
}

final class InboxSyncStoreTests: XCTestCase {
    private func file() -> URL {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        addTeardownBlock { try? FileManager.default.removeItem(at: dir) }
        return dir.appendingPathComponent("inbox.sqlite")
    }
    func testPageAndCheckpointCommitTogetherAndResumeAfterRestart() async throws {
        let file = file(), store = InboxStore(fileURL: file)
        try await store.activate(scope: "a")
        try await store.applySyncPage([syncItem("one")], cursor: "one", expectedCursor: nil, scope: "a")
        let cursor = try await InboxStore(fileURL: file).syncCursor(scope: "a")
        XCTAssertEqual(cursor, "one")
        var db: OpaquePointer?
        XCTAssertEqual(sqlite3_open(file.path, &db), SQLITE_OK)
        defer { sqlite3_close(db) }
        XCTAssertEqual(sqlite3_exec(db, "CREATE TRIGGER fail_page BEFORE INSERT ON inbox_messages WHEN NEW.id='bad' BEGIN SELECT RAISE(ABORT,'write failure'); END", nil, nil, nil), SQLITE_OK)
        do {
            try await store.applySyncPage([syncItem("two"), syncItem("bad")], cursor: "bad", expectedCursor: "one", scope: "a")
            XCTFail("partial page must roll back")
        } catch {}
        let after = try await store.syncCursor(scope: "a"), rows = try await store.load(scope: "a")
        XCTAssertEqual(after, "one"); XCTAssertEqual(rows?.map(\.id), ["one"])
    }
    func testStaleCursorScopeAndResetCannotWrite() async throws {
        let store = InboxStore(fileURL: file())
        try await store.activate(scope: "a")
        try await store.applySyncPage([syncItem("one")], cursor: "new", expectedCursor: nil, scope: "a")
        do { try await store.applySyncPage([syncItem("stale")], cursor: "old", expectedCursor: nil, scope: "a"); XCTFail("stale cursor") } catch {}
        try await store.activate(scope: "b")
        do { try await store.applySyncPage([syncItem("foreign")], cursor: "x", expectedCursor: "new", scope: "a"); XCTFail("stale scope") } catch {}
        let saved = try await store.load(scope: "a")
        XCTAssertEqual(saved?.map(\.id), ["one"])
        try await store.activate(scope: "a")
        do { try await store.applySyncPage([syncItem("reset")], cursor: "x", expectedCursor: "new", scope: "a", isCurrent: { false }); XCTFail("registration changed") } catch {}
        try await store.erase(scope: "a")
        let cursor = try await store.syncCursor(scope: "a")
        XCTAssertNil(cursor)
    }
    func testReadStateTombstonesAndOmittedHistorySurviveSync() async throws {
        let store = InboxStore(fileURL: file())
        try await store.activate(scope: "a")
        try await store.applySyncPage([syncItem("read"), syncItem("keep"), syncItem("deleted")], cursor: "first", expectedCursor: nil, scope: "a")
        try await store.markLocalRead(["read"], scope: "a")
        try await store.applyChanges([], deleting: ["deleted"], scope: "a")
        try await store.applySyncPage([syncItem("read"), syncItem("deleted"), syncItem("fresh", read: true)], cursor: "second", expectedCursor: "first", scope: "a")
        let rows = try await store.load(scope: "a") ?? []
        XCTAssertEqual(Set(rows.map(\.id)), ["read", "keep", "fresh"])
        XCTAssertEqual(rows.first { $0.id == "read" }?.isUnread, false)
        XCTAssertEqual(rows.first { $0.id == "fresh" }?.isUnread, true, "server read flags must not override local unread authority")
    }
}

extension InboxSyncStoreTests {
    func testStalePendingSnapshotPreservesResolvedLegacyHistory() async throws {
        let store = InboxStore(fileURL: file())
        try await store.activate(scope: "a")
        var resolved = syncItem("legacy")
        resolved.question = .init(id: "old-question", status: .answered, answer: "saved text", expiresAt: .distantPast)
        try await store.receiveVerified(resolved, scope: "a")
        var stale = resolved
        stale.question?.status = .pending
        stale.question?.answer = nil
        var unseen = stale
        unseen.id = "unseen"
        try await store.applySyncPage([stale, unseen], cursor: "first", expectedCursor: nil, scope: "a")
        let rows = try await store.load(scope: "a") ?? []
        XCTAssertEqual(rows.first { $0.id == "legacy" }?.question?.status, .answered)
        XCTAssertEqual(rows.first { $0.id == "legacy" }?.question?.answer, "saved text")
        XCTAssertEqual(rows.first { $0.id == "unseen" }?.question?.status, .pending)
    }

    func testUnchangedLocalHistoryAvoidsDecodeButOtherConnectionsAndReadsInvalidate() async throws {
        let file = file(), store = InboxStore(fileURL: file)
        try await store.activate(scope: "a")
        try await store.applySyncPage([syncItem("one")], cursor: "first", expectedCursor: nil, scope: "a")
        let initial = try await store.changedSnapshot(scope: "a", since: nil)
        let first = try XCTUnwrap(initial)
        let unchanged = try await store.changedSnapshot(scope: "a", since: first.revision)
        XCTAssertNil(unchanged)
        try await store.applySyncPage([], cursor: "same-data", expectedCursor: "first", scope: "a")
        let metadataOnly = try await store.changedSnapshot(scope: "a", since: first.revision)
        XCTAssertNil(metadataOnly, "a sync checkpoint alone must not decode all history")
        let other = InboxStore(fileURL: file)
        try await other.markLocalRead(["one"], scope: "a")
        let read = try await store.changedSnapshot(scope: "a", since: first.revision)
        XCTAssertNotNil(read)
        XCTAssertEqual(read?.items.first?.isUnread, false)
        if let read {
            try await other.receiveVerified(syncItem("two"), scope: "a")
            let arrival = try await store.changedSnapshot(scope: "a", since: read.revision)
            XCTAssertEqual(arrival?.items.count, 2)
        }
    }
}
