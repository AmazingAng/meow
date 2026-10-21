import XCTest
import SQLite3
@testable import Meow

private final class CacheRetentionProtocol: URLProtocol, @unchecked Sendable {
    static let lock = NSLock()
    static var handler: ((URLRequest) throws -> (Int, Data))?
    static func install(_ handler: @escaping (URLRequest) throws -> (Int, Data)) {
        lock.withLock { Self.handler = handler }
        URLProtocol.registerClass(Self.self)
    }
    override class func canInit(with request: URLRequest) -> Bool { request.url?.host == "cache-retention.invalid" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        DispatchQueue.global().async {
            do {
                guard let handler = Self.lock.withLock({ Self.handler }) else { throw URLError(.cancelled) }
                let (status, data) = try handler(self.request)
                self.client?.urlProtocol(self, didReceive: HTTPURLResponse(url: self.request.url!, statusCode: status,
                    httpVersion: nil, headerFields: nil)!, cacheStoragePolicy: .notAllowed)
                self.client?.urlProtocol(self, didLoad: data)
                self.client?.urlProtocolDidFinishLoading(self)
            } catch { self.client?.urlProtocol(self, didFailWithError: error) }
        }
    }
    override func stopLoading() {}
}

@MainActor
final class CacheRetentionTests: XCTestCase {
    private let server = URL(string: "https://cache-retention.invalid")!
    private let token = "mdt_cache_fixture"
    private var previousServer: URL?, previousToken: String?, previousDevice: String?
    private var folder: URL!
    private var store: InboxStore!
    private var scope: String { InboxStore.scope(server: server, token: token) }
    private let missing = Data(#"{"error":{"code":"not_found","message":"notification not found"}}"#.utf8)

    override func setUp() async throws {
        previousServer = MeowShared.server; previousToken = MeowShared.deviceToken; previousDevice = MeowShared.deviceID
        MeowShared.server = server; MeowShared.deviceToken = token; MeowShared.deviceID = "dev_cache"
        folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
    }
    override func tearDown() async throws {
        URLProtocol.unregisterClass(CacheRetentionProtocol.self)
        CacheRetentionProtocol.lock.withLock { CacheRetentionProtocol.handler = nil }
        MeowShared.server = previousServer; MeowShared.deviceToken = previousToken; MeowShared.deviceID = previousDevice
        try? FileManager.default.removeItem(at: folder)
    }
    private func item(_ id: String, read: Bool = false) -> MeowNotification {
        MeowNotification(id: id, type: .info, title: "历史测试 \(id)", body: "**完整正文** 😺\n```go\nrun()\n```",
            source: "CI", priority: "normal", pushStatus: "accepted", createdAt: Date(timeIntervalSince1970: 1_700_000_000),
            readAt: read ? Date(timeIntervalSince1970: 1_700_000_010) : nil)
    }
    private func page(_ items: [MeowNotification]) throws -> Data {
        let encoder = JSONEncoder(); encoder.dateEncodingStrategy = .iso8601; encoder.keyEncodingStrategy = .convertToSnakeCase
        return try encoder.encode(InboxPage(notifications: items))
    }
    private func remote(_ items: [MeowNotification]) throws {
        let data = try page(items), missing = missing
        let counts = Data("{\"unread\":\(items.filter(\.isUnread).count),\"pending\":0}".utf8)
        CacheRetentionProtocol.install { request in
            if request.url!.path == "/v1/inbox" && request.httpMethod == "DELETE" { return (200, Data(#"{"deleted":0}"#.utf8)) }
            if request.url!.path.hasSuffix("/counts") { return (200, counts) }
            if request.url!.path == "/v1/inbox" { return (200, data) }
            return (404, missing)
        }
    }

    func testServerExpiryDoesNotDeleteLocalHistoryAndSurvivesRestart() async throws {
        let original = item("old")
        try await store.save([original], scope: scope)
        try remote([item("new")])
        let model = AppModel(inboxStore: store)
        await model.refresh()
        XCTAssertEqual(Set(model.savedNotifications.map(\.id)), ["old", "new"])
        XCTAssertEqual(model.notification(id: "old")?.body, original.body)
        XCTAssertEqual(model.counts.unread, 2)
        try remote([])
        let reopened = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
        let restarted = AppModel(inboxStore: reopened)
        await restarted.refresh()
        restarted.search = "old"
        XCTAssertEqual(restarted.notifications.map(\.id), ["old"])
        XCTAssertEqual(restarted.counts.unread, 2, "server zero must not hide unread local history")
        let disk = try await reopened.load(scope: scope)
        XCTAssertEqual(disk?.count, 2)
    }

    func testCached404DetailIsStillReadableButUnknown404IsNot() async throws {
        try await store.save([item("old")], scope: scope)
        try remote([])
        let model = AppModel(inboxStore: store)
        let found = await model.loadDetail("old")
        XCTAssertTrue(found)
        XCTAssertEqual(model.notification(id: "old")?.body, item("old").body)
        let unknown = await model.loadDetail("unknown")
        XCTAssertFalse(unknown)
        let disk = try await store.load(scope: scope)
        XCTAssertEqual(disk?.map(\.id), ["old"])
    }

    func testExpiredMessageCanBeReadAndDeletedLocallyWithoutReturningAfterRestart() async throws {
        try await store.save([item("old")], scope: scope)
        try remote([])
        let model = AppModel(inboxStore: store)
        await model.refresh()
        await model.markRead("old")
        XCTAssertEqual(model.notification(id: "old")?.isUnread, false)
        XCTAssertNil(model.toast)
        let read = try await store.load(scope: scope)
        XCTAssertEqual(read?.first?.isUnread, false)
        await model.deleteNotification("old")
        XCTAssertNil(model.notification(id: "old"))
        XCTAssertNil(model.alert)
        let restarted = AppModel(inboxStore: InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite")))
        await restarted.refresh()
        XCTAssertTrue(restarted.savedNotifications.isEmpty)
    }

    func testRangeDeleteClearsLocalOnlyHistoryWhenServerDeletesZero() async throws {
        try await store.save([item("old"), item("also-old")], scope: scope)
        try remote([])
        let model = AppModel(inboxStore: store)
        await model.refresh()
        await model.deleteNotifications(in: InboxDeleteRange.all.interval())
        XCTAssertTrue(model.savedNotifications.isEmpty)
        let disk = try await store.load(scope: scope)
        XCTAssertEqual(disk, [])
    }

    func testReadStateCannotRegressOnStaleServerRefreshOrDetail() async throws {
        try await store.save([item("saved", read: true)], scope: scope)
        try remote([item("saved")])
        let model = AppModel(inboxStore: store)
        await model.refresh()
        XCTAssertEqual(model.notification(id: "saved")?.isUnread, false)
        XCTAssertEqual(model.counts.unread, 0)
        let data = try JSONEncoder.meowCacheTest.encode(item("saved"))
        CacheRetentionProtocol.install { _ in (200, data) }
        let found = await model.loadDetail("saved")
        XCTAssertTrue(found)
        XCTAssertEqual(model.notification(id: "saved")?.isUnread, false)
    }

    func testRefreshedCountsIncludeExpiredUnreadHistoryAndMarkAllReadPersistsIt() async throws {
        let old = item("old")
        try remote([old])
        let model = AppModel(inboxStore: store)
        await model.refresh()
        try remote([])
        await model.refreshCounts()
        XCTAssertEqual(model.counts.unread, 1)
        XCTAssertNotNil(model.notification(id: "old"))
        CacheRetentionProtocol.install { _ in (200, Data(#"{"marked":0}"#.utf8)) }
        await model.markAllRead()
        XCTAssertEqual(model.notification(id: "old")?.isUnread, false)
        XCTAssertEqual(model.counts.unread, 0)
        let disk = try await store.load(scope: scope)
        XCTAssertEqual(disk?.first?.isUnread, false)
    }

    func testGeneratedSnapshotsMergeWithoutLosingOrDuplicatingHistory() async throws {
        for offset in 0..<12 {
            let local = (0..<20).map { item("id-\($0)", read: $0 % 3 == 0) }
            let incoming = (offset..<(offset + 20)).map { item("id-\($0)") }
            try await store.save(local, scope: scope)
            try remote(incoming.reversed())
            let model = AppModel(inboxStore: store)
            await model.refresh()
            let expected = Set((local + incoming).map(\.id))
            XCTAssertEqual(Set(model.savedNotifications.map(\.id)), expected)
            XCTAssertEqual(model.savedNotifications.count, expected.count)
            for saved in local where !saved.isUnread {
                XCTAssertEqual(model.notification(id: saved.id)?.isUnread, false)
            }
            let disk = try await store.load(scope: scope)
            XCTAssertEqual(Set(disk?.map(\.id) ?? []), expected)
        }
    }

    func testResolvedLegacyQuestionCannotRegressOnServerRefreshOrDetail() async throws {
        var resolved = item("question", read: true)
        resolved.type = .question
        resolved.question = .init(id: "q_cached", status: .answered, choices: nil, answer: "done",
                                  expiresAt: .now.addingTimeInterval(600))
        var older = resolved
        older.readAt = nil
        older.question?.status = .pending
        older.question?.answer = nil
        try await store.save([resolved], scope: scope)
        try remote([older])
        let model = AppModel(inboxStore: store)
        await model.refresh()
        XCTAssertEqual(model.notification(id: resolved.id)?.question?.status, .answered)
        let data = try JSONEncoder.meowCacheTest.encode(older)
        CacheRetentionProtocol.install { _ in (200, data) }
        let found = await model.loadDetail(resolved.id)
        XCTAssertTrue(found)
        XCTAssertEqual(model.notification(id: resolved.id)?.question?.answer, "done")
    }

    func testCachedPendingLegacyQuestionIsIncludedInLocalCounts() async throws {
        var pending = item("legacy")
        pending.type = .question
        pending.question = .init(id: "q_pending", status: .pending, choices: nil, answer: nil,
                                 expiresAt: .now.addingTimeInterval(600))
        try await store.save([pending], scope: scope)
        try remote([])
        let model = AppModel(inboxStore: store)
        await model.refresh()
        XCTAssertEqual(model.counts.pending, 1)
        XCTAssertEqual(model.counts.unread, 1)
    }

    func testFailedHistoryReadNeverPromotesPartialServerCacheToCompleteLocalHistory() async throws {
        try await store.save([item("old"), item("broken")], scope: scope)
        var db: OpaquePointer?
        XCTAssertEqual(sqlite3_open(folder.appendingPathComponent("inbox.sqlite").path, &db), SQLITE_OK)
        defer { sqlite3_close(db) }
        XCTAssertEqual(sqlite3_exec(db, "UPDATE inbox_messages SET payload=X'' WHERE id='broken'", nil, nil, nil), SQLITE_OK)
        try remote([item("fresh")])
        let model = AppModel(inboxStore: store)
        await model.refresh()
        XCTAssertNotNil(model.notification(id: "fresh"), "remote content remains usable")
        // Drain any queued disk work, even when decoding the damaged row fails.
        do { _ = try await store.load(scope: scope) } catch {}
        var query: OpaquePointer?
        XCTAssertEqual(sqlite3_prepare_v2(db, "SELECT count(*) FROM inbox_messages", -1, &query, nil), SQLITE_OK)
        XCTAssertEqual(sqlite3_step(query), SQLITE_ROW)
        XCTAssertEqual(sqlite3_column_int(query, 0), 2, "a failed local read must not authorize deleting saved history")
        sqlite3_finalize(query)
        XCTAssertNotNil(model.inboxStorageError)
        let repaired = try JSONEncoder().encode(item("broken")).map { String(format: "%02x", $0) }.joined()
        XCTAssertEqual(sqlite3_exec(db, "UPDATE inbox_messages SET payload=X'\(repaired)' WHERE id='broken'", nil, nil, nil), SQLITE_OK)
        await model.refresh()
        XCTAssertEqual(Set(model.savedNotifications.map(\.id)), ["old", "broken", "fresh"])
        let recovered = try await store.load(scope: scope)
        XCTAssertEqual(recovered?.count, 3)
    }

    func testServerUnavailableDoesNotPermitDeletingSavedHistory() async throws {
        let original = item("old")
        try await store.save([original], scope: scope)
        try remote([])
        let model = AppModel(inboxStore: store)
        await model.refresh()
        for status in [401, 404, 500] {
            CacheRetentionProtocol.install { _ in (status, Data(#"{"error":{"code":"unsupported","message":"unavailable"}}"#.utf8)) }
            await model.deleteNotification("old")
            XCTAssertEqual(model.notification(id: "old"), original)
            XCTAssertNotNil(model.alert)
        }
    }
}

private extension JSONEncoder {
    static var meowCacheTest: JSONEncoder {
        let encoder = JSONEncoder(); encoder.dateEncodingStrategy = .iso8601; encoder.keyEncodingStrategy = .convertToSnakeCase
        return encoder
    }
}
