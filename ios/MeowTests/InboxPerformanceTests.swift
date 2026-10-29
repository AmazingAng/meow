import SQLite3
import XCTest

@testable import Meow

private func performanceItem(_ i: Int) -> MeowNotification {
    MeowNotification(
        id: "ntf_perf_\(i)", type: .info, title: i == 0 ? "Search target" : "fixture \(i)", body: "正文", source: "Codex",
        priority: "normal", pushStatus: "accepted", createdAt: Date(timeIntervalSince1970: 1_790_000_000 + Double(i)),
        context: .init(workspace: "project", session: "session", sessionId: "stable"), event: .result)
}

private final class InboxPerformanceProtocol: URLProtocol, @unchecked Sendable {
    static let lock = NSLock()
    nonisolated(unsafe) static var items: [MeowNotification] = []
    nonisolated(unsafe) static var requests: [URLRequest] = []
    nonisolated(unsafe) static var unsupported = false
    nonisolated(unsafe) static var rateLimits = 0
    nonisolated(unsafe) static var invalidAcknowledgement = false
    nonisolated(unsafe) static var failingID: String?
    override class func canInit(with request: URLRequest) -> Bool { request.url?.host == "ios-performance.invalid" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        var observed = request
        if observed.httpBody == nil, let stream = observed.httpBodyStream {
            stream.open()
            defer { stream.close() }
            var data = Data()
            var buffer = [UInt8](repeating: 0, count: 4096)
            while stream.hasBytesAvailable {
                let n = stream.read(&buffer, maxLength: buffer.count)
                if n <= 0 { break }
                data.append(contentsOf: buffer.prefix(n))
            }
            observed.httpBody = data
        }
        let (status, data, delay): (Int, Data, Int) = Self.lock.withLock {
            Self.requests.append(observed)
            let path = request.url!.path
            let encoder = JSONEncoder()
            encoder.keyEncodingStrategy = .convertToSnakeCase
            encoder.dateEncodingStrategy = .iso8601
            if path.hasSuffix("/read-selected") {
                if Self.rateLimits > 0 {
                    Self.rateLimits -= 1
                    return (429, Data(#"{"error":{"code":"rate_limited","message":"wait"}}"#.utf8), 0)
                }
                if Self.invalidAcknowledgement { return (200, Data(#"{"marked":9999}"#.utf8), 0) }

                if Self.unsupported {
                    return (404, Data(#"{"error":{"code":"not_found","message":"no such endpoint"}}"#.utf8), 0)
                }
                let json = (try? JSONSerialization.jsonObject(with: observed.httpBody ?? Data())) as? [String: Any]
                return (200, Data("{\"marked\":\((json?["ids"] as? [String])?.count ?? 0)}".utf8), 0)
            }
            if path.hasSuffix("/read") {
                return path.contains(Self.failingID ?? "not-a-notification-id") ? (503, Data(), 0) : (204, Data(), 0)
            }
            if path.hasSuffix("/counts") {
                return (200, Data("{\"unread\":\(Self.items.count),\"pending\":0}".utf8), 0)
            }
            if path == "/v1/inbox" {
                let cursor =
                    URLComponents(url: request.url!, resolvingAgainstBaseURL: false)?.queryItems?.first(where: {
                        $0.name == "cursor"
                    })?.value.flatMap(Int.init) ?? 0
                let end = min(cursor + 200, Self.items.count)
                return (
                    200,
                    try! encoder.encode(
                        InboxPage(
                            notifications: Array(Self.items[cursor..<end]),
                            nextCursor: end < Self.items.count ? String(end) : nil)), 80
                )
            }
            if path == "/v1/notifications/ntf_perf_9999" { return (200, try! encoder.encode(performanceItem(9999)), 0) }
            return (404, Data(#"{"error":{"code":"not_found","message":"absent"}}"#.utf8), 0)
        }
        DispatchQueue.global().asyncAfter(deadline: .now() + .milliseconds(delay)) { [self] in
            client?.urlProtocol(
                self,
                didReceive: HTTPURLResponse(
                    url: request.url!, statusCode: status, httpVersion: nil,
                    headerFields: status == 429 ? ["Retry-After": "1"] : nil)!,
                cacheStoragePolicy: .notAllowed)
            if !data.isEmpty { client?.urlProtocol(self, didLoad: data) }
            client?.urlProtocolDidFinishLoading(self)
        }
    }
    override func stopLoading() {}
}

@MainActor
final class InboxPerformanceTests: XCTestCase {
    private let server = URL(string: "https://ios-performance.invalid")!
    private let token = "mdt_disposable_ios_performance"
    private var oldServer: URL?, oldToken: String?, oldDevice: String?
    private var folder: URL!
    private var store: InboxStore!
    private var scope: String { InboxStore.scope(server: server, token: token) }
    override func setUp() async throws {
        oldServer = MeowShared.server
        oldToken = MeowShared.deviceToken
        oldDevice = MeowShared.deviceID
        MeowShared.server = server
        MeowShared.deviceToken = token
        MeowShared.deviceID = "dev_perf"
        folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
        InboxPerformanceProtocol.lock.withLock {
            InboxPerformanceProtocol.items = (0..<500).map(performanceItem)
            InboxPerformanceProtocol.requests = []
            InboxPerformanceProtocol.unsupported = false
            InboxPerformanceProtocol.rateLimits = 0
            InboxPerformanceProtocol.invalidAcknowledgement = false
            InboxPerformanceProtocol.failingID = nil
        }
        URLProtocol.registerClass(InboxPerformanceProtocol.self)
    }
    override func tearDown() async throws {
        URLProtocol.unregisterClass(InboxPerformanceProtocol.self)
        MeowShared.server = oldServer
        MeowShared.deviceToken = oldToken
        MeowShared.deviceID = oldDevice
        try? FileManager.default.removeItem(at: folder)
    }
    private var requests: [URLRequest] { InboxPerformanceProtocol.lock.withLock { InboxPerformanceProtocol.requests } }
    private func model() -> AppModel {
        let defaults = UserDefaults(suiteName: UUID().uuidString)!
        return AppModel(inboxStore: store, groupingStore: InboxGroupingStore(defaults: defaults))
    }

    func testSearchUsesBackgroundProjectionWithoutNetworkOrStaleRows() async throws {
        let model = model()
        await model.refresh()
        let baseline = requests.count
        model.search = "Search target"
        XCTAssertTrue(model.notifications.isEmpty, "hide stale rows until background projection completes")
        await model.waitForInboxGrouping()
        XCTAssertEqual(model.notifications.map(\.id), ["ntf_perf_0"])
        for _ in 0..<10 {
            _ = model.notifications
            _ = model.nextCursor
        }
        XCTAssertEqual(requests.count, baseline)
        model.search = "does not exist"
        model.search = "project"
        await model.waitForInboxGrouping()
        XCTAssertEqual(model.notifications.count, 50)
        XCTAssertNotNil(model.nextCursor)
    }

    func testOverlappingRefreshesShareOneDownload() async throws {
        let model = model()
        let first = Task { await model.refresh() }
        let until = Date().addingTimeInterval(3)
        while requests.filter({ $0.url?.path == "/v1/inbox" }).isEmpty {
            if Date() > until {
                XCTFail("first sync did not start")
                break
            }
            try await Task.sleep(for: .milliseconds(1))
        }
        let second = Task { await model.refresh() }
        let third = Task { await model.refresh() }
        await first.value
        await second.value
        await third.value
        XCTAssertEqual(requests.filter({ $0.url?.path == "/v1/inbox" }).count, 3)
        XCTAssertEqual(model.savedNotifications.count, 500)
    }

    func testVerifiedForegroundNotificationFetchesOnlyItsRecord() async throws {
        let model = model()
        await model.refresh()
        let before = requests.count
        await model.receiveNotification("ntf_perf_9999", identity: NotificationIdentity.current!)
        let added = Array(requests.dropFirst(before))
        XCTAssertEqual(added.map { $0.url!.path }, ["/v1/notifications/ntf_perf_9999"])
        XCTAssertNotNil(model.notification(id: "ntf_perf_9999"))
        XCTAssertEqual(model.savedNotifications.count, 501)
    }

    func testBulkReadOfRemoteAndLocalOnlyHistoryNeverSendsHTTP() async throws {
        let items = (0..<1000).map(performanceItem)
        try await store.save(items, scope: scope)
        let model = model(); await model.refresh()
        let before = requests.count
        await model.markMatchingRead(Set(items.map(\.id)))
        XCTAssertEqual(requests.count, before)
        XCTAssertFalse(model.savedNotifications.contains(where: \.isUnread))
        XCTAssertTrue(model.failedGroupReadIDs.isEmpty)
    }

    func testOldOrFailingServerCannotPreventLocalReads() async throws {
        InboxPerformanceProtocol.lock.withLock {
            InboxPerformanceProtocol.items = Array(InboxPerformanceProtocol.items.prefix(3))
            InboxPerformanceProtocol.unsupported = true; InboxPerformanceProtocol.failingID = "ntf_perf_1"
        }
        let model = model(); await model.refresh()
        let before = requests.count
        await model.markMatchingRead(Set(model.savedNotifications.map(\.id)))
        XCTAssertTrue(model.failedGroupReadIDs.isEmpty)
        XCTAssertFalse(model.savedNotifications.contains(where: \.isUnread))
        XCTAssertEqual(requests.count, before)
    }
    func testFailedLocalReadTransactionCanRetryWithoutPretendingSuccess() async throws {
        let model = model(); await model.refresh()
        var db: OpaquePointer?
        XCTAssertEqual(sqlite3_open(folder.appendingPathComponent("inbox.sqlite").path, &db), SQLITE_OK)
        defer { sqlite3_close(db) }
        XCTAssertEqual(sqlite3_exec(db, "CREATE TRIGGER fail_write BEFORE UPDATE OF read_at ON inbox_messages BEGIN SELECT RAISE(ABORT,'injected'); END", nil, nil, nil), SQLITE_OK)
        let before = requests.count
        let failed = await model.markRead("ntf_perf_0"); XCTAssertFalse(failed)
        XCTAssertNotNil(model.inboxStorageError)
        XCTAssertTrue(try XCTUnwrap(model.notification(id: "ntf_perf_0")).isUnread)
        let durable = try await store.load(scope: scope)
        XCTAssertTrue(try XCTUnwrap(durable?.first(where: { $0.id == "ntf_perf_0" })).isUnread)
        XCTAssertEqual(sqlite3_exec(db, "DROP TRIGGER fail_write", nil, nil, nil), SQLITE_OK)
        let retried = await model.markRead("ntf_perf_0"); XCTAssertTrue(retried)
        XCTAssertNil(model.inboxStorageError, "a successful local write must clear its recovered error")
        XCTAssertFalse(try XCTUnwrap(model.notification(id: "ntf_perf_0")).isUnread)
        XCTAssertEqual(requests.count, before)
    }

    func testReadMutationDoesNotDecodeUnrelatedMessageBodies() async throws {
        let model = model(); await model.refresh()
        var db: OpaquePointer?
        XCTAssertEqual(sqlite3_open(folder.appendingPathComponent("inbox.sqlite").path, &db), SQLITE_OK)
        defer { sqlite3_close(db) }
        // An unrelated opaque payload can be unreadable without blocking the
        // indexed read-state transaction for an already-loaded healthy message.
        XCTAssertEqual(sqlite3_exec(db, "UPDATE inbox_messages SET payload=x'00' WHERE id='ntf_perf_1'", nil, nil, nil), SQLITE_OK)
        let before = requests.count
        let read = await model.markRead("ntf_perf_0"); XCTAssertTrue(read)
        XCTAssertFalse(try XCTUnwrap(model.notification(id: "ntf_perf_0")).isUnread)
        XCTAssertNil(model.inboxStorageError, "read mutation must not decode unrelated bodies")
        let unread = try await store.unreadCount(scope: scope); XCTAssertEqual(unread, 499)
        XCTAssertEqual(requests.count, before)
    }

    func testCachedGettersAndGeneratedSearchAgreeWithCompleteLocalHistory() async throws {
        var items = (0..<10000).map(performanceItem)
        for i in items.indices {
            items[i].body = String(repeating: "正文", count: 512)
            if i % 3 == 0 { items[i].readAt = .now }
        }
        try await store.save(items, scope: scope)
        InboxPerformanceProtocol.lock.withLock { InboxPerformanceProtocol.items = [] }
        let model = model()
        await model.refresh()
        let baseline = requests.count
        for query in ["fixture 9999", "Search target", "project", "missing", "正文"] {
            model.search = query
            model.filter = .unread
            await model.waitForInboxGrouping()
            let expected = model.savedNotifications.filter { InboxGrouping.matches($0, query: query, unreadOnly: true) }
            XCTAssertEqual(model.notifications.map(\.id), Array(expected.prefix(50)).map(\.id))
        }
        let start = Date()
        for _ in 0..<1000 {
            _ = model.notifications
            _ = model.nextCursor
        }
        let elapsed = Date().timeIntervalSince(start)
        print("PERFORMANCE cached_getters_1000_ms=\(elapsed * 1000) records=10000")
        XCTAssertLessThan(elapsed, 1, "cached getters must not rescan all 10MB bodies")
        XCTAssertEqual(requests.count, baseline)
        let reset = await model.reset(server: URL(string: "https://other.invalid")!)
        XCTAssertTrue(reset)
        await model.waitForInboxGrouping()
        XCTAssertTrue(model.notifications.isEmpty)
    }

    func testLocalReadIgnoresServerRateLimitAndInvalidAcknowledgement() async throws {
        InboxPerformanceProtocol.lock.withLock {
            InboxPerformanceProtocol.items = [performanceItem(0)]
            InboxPerformanceProtocol.rateLimits = 99
            InboxPerformanceProtocol.invalidAcknowledgement = true
        }
        let model = model(); await model.refresh()
        let before = requests.count
        await model.markMatchingRead(["ntf_perf_0"])
        XCTAssertEqual(requests.count, before)
        XCTAssertTrue(model.failedGroupReadIDs.isEmpty)
        XCTAssertFalse(try XCTUnwrap(model.notification(id: "ntf_perf_0")).isUnread)
    }

    func testProjectionExpiryAndCancelledWorkCannotPublishStaleMatches() async throws {
        let now = Date()
        var live = performanceItem(0)
        var expired = performanceItem(1)
        live.question = .init(id: "q_live", status: .pending, expiresAt: now.addingTimeInterval(10))
        expired.question = .init(id: "q_expired", status: .pending, expiresAt: now.addingTimeInterval(-10))
        let pending = InboxProjection.build([live, expired], query: "", unreadOnly: false, pendingOnly: true, now: now)
        XCTAssertEqual(pending.matches.map(\.id), [live.id])
        let after = InboxProjection.build(
            [live, expired], query: "", unreadOnly: false, pendingOnly: true, now: now.addingTimeInterval(11))
        XCTAssertTrue(after.matches.isEmpty)
        let cancelled = Task { () -> InboxProjection in
            withUnsafeCurrentTask { $0?.cancel() }
            return InboxProjection.build([live], query: "fixture", unreadOnly: false, pendingOnly: false, now: now)
        }
        let result = await cancelled.value
        XCTAssertTrue(result.matches.isEmpty)
    }

    func testForegroundArrivalDuringFullSyncRetainsItsRemoteMembership() async throws {
        let model = model()
        await model.refresh()
        let before = requests.count
        let syncing = Task { await model.refresh() }
        let until = Date().addingTimeInterval(3)
        while !requests.dropFirst(before).contains(where: { $0.url?.path == "/v1/inbox" }) && Date() < until {
            try await Task.sleep(for: .milliseconds(1))
        }
        await model.receiveNotification("ntf_perf_9999", identity: NotificationIdentity.current!)
        await syncing.value
        let beforeRead = requests.count
        await model.markMatchingRead(["ntf_perf_9999"])
        XCTAssertEqual(requests.count, beforeRead)
        XCTAssertFalse(try XCTUnwrap(model.notification(id: "ntf_perf_9999")).isUnread)
    }

}
