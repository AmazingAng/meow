import XCTest
import SQLite3
@testable import Meow

private func localItem(_ id: String, title: String = "测试通知", read: Bool = false,
                       question: MeowNotification.Question? = nil) -> MeowNotification {
    MeowNotification(id: id, type: question == nil ? .info : .question, title: title,
        body: "中文正文 100% _ literal ' SQL", source: "Codex", priority: "normal",
        pushStatus: "accepted", createdAt: Date(timeIntervalSince1970: 1_790_000_000),
        readAt: read ? .now : nil, question: question)
}

final class InboxStoreTests: XCTestCase {
    private func database() -> URL {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        addTeardownBlock { try? FileManager.default.removeItem(at: folder) }
        return folder.appendingPathComponent("inbox.sqlite")
    }

    func testSnapshotSurvivesRestartAndRetainsCompleteQuestionAndReadState() async throws {
        let file = database()
        let item = localItem("one", read: true, question: .init(id: "q_one", status: .answered,
            choices: ["approve", "reject"], answer: "approve", expiresAt: .now.addingTimeInterval(60)))
        try await InboxStore(fileURL: file).save([item], scope: "device-a")
        let restored = try await InboxStore(fileURL: file).load(scope: "device-a")
        XCTAssertEqual(restored, [item])
    }

    func testScopesAreStableNormalizedAndIsolatedWithoutStoringSecrets() async throws {
        let a = InboxStore.scope(server: URL(string: "https://a.example.com/")!, token: "mdt_private_a")
        let same = InboxStore.scope(server: URL(string: "https://a.example.com")!, token: "mdt_private_a")
        let b = InboxStore.scope(server: URL(string: "https://b.example.com")!, token: "mdt_private_a")
        let rotated = InboxStore.scope(server: URL(string: "https://a.example.com")!, token: "mdt_private_b")
        XCTAssertEqual(a, same)
        XCTAssertEqual(Set([a, b, rotated]).count, 3)
        XCTAssertFalse(a.contains("mdt_private_a"))
        let file = database()
        let store = InboxStore(fileURL: file)
        try await store.save([localItem("secret-scope")], scope: a)
        let other = try await store.load(scope: b)
        XCTAssertNil(other)
        XCTAssertNil(try Data(contentsOf: file).range(of: Data("mdt_private_a".utf8)))
    }

    func testCompleteSnapshotRemovesStaleRowsAndDistinguishesEmptyFromNeverSynced() async throws {
        let store = InboxStore(fileURL: database())
        let missing = try await store.load(scope: "a")
        XCTAssertNil(missing)
        try await store.save([localItem("one"), localItem("two")], scope: "a")
        try await store.save([localItem("two", title: "Updated")], scope: "a")
        let updated = try await store.load(scope: "a")
        XCTAssertEqual(updated, [localItem("two", title: "Updated")])
        try await store.save([], scope: "a")
        let empty = try await store.load(scope: "a")
        XCTAssertEqual(empty, [])
        try await store.erase(scope: "a")
        let erased = try await store.load(scope: "a")
        XCTAssertNil(erased)
    }

    func testInvalidSnapshotDoesNotDestroyPreviousTransaction() async throws {
        let store = InboxStore(fileURL: database())
        let original = localItem("original")
        try await store.save([original], scope: "a")
        var invalid = localItem("bad")
        invalid.createdAt = Date(timeIntervalSince1970: .infinity)
        do { try await store.save([invalid], scope: "a"); XCTFail("invalid dates must fail the transaction") }
        catch {}
        let restored = try await store.load(scope: "a")
        XCTAssertEqual(restored, [original])
    }

    func testGeneratedUnicodeRecordsRoundTripAndConcurrentScopesDoNotMix() async throws {
        let store = InboxStore(fileURL: database())
        try await withThrowingTaskGroup(of: Void.self) { group in
            for index in 0..<32 {
                group.addTask {
                    let item = localItem("id_\(index)", title: "测试 😺 '\(index)%_ café e\u{301}")
                    try await store.save([item], scope: "scope_\(index)")
                    let restored = try await store.load(scope: "scope_\(index)")
                    XCTAssertEqual(restored, [item])
                }
            }
            try await group.waitForAll()
        }
    }
}

private final class LocalInboxProtocol: URLProtocol, @unchecked Sendable {
    static let lock = NSLock()
    static var handler: ((URLRequest) throws -> (Int, Data))?
    static func install(_ handler: @escaping (URLRequest) throws -> (Int, Data)) {
        lock.withLock { Self.handler = handler }
        URLProtocol.registerClass(Self.self)
    }
    override class func canInit(with request: URLRequest) -> Bool { request.url?.host == "local-inbox.invalid" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        DispatchQueue.global().async {
            do {
                guard let handler = Self.lock.withLock({ Self.handler }) else { throw URLError(.cancelled) }
                let (status, data) = try handler(self.request)
                self.client?.urlProtocol(self, didReceive: HTTPURLResponse(url: self.request.url!, statusCode: status,
                    httpVersion: nil, headerFields: status == 429 ? ["Retry-After": "1"] : nil)!, cacheStoragePolicy: .notAllowed)
                self.client?.urlProtocol(self, didLoad: data)
                self.client?.urlProtocolDidFinishLoading(self)
            } catch { self.client?.urlProtocol(self, didFailWithError: error) }
        }
    }
    override func stopLoading() {}
}

@MainActor
final class LocalInboxModelTests: XCTestCase {
    private let server = URL(string: "https://local-inbox.invalid")!
    private let token = "mdt_local_fixture"
    private var previousServer: URL?
    private var previousToken: String?
    private var previousDevice: String?
    private var folder: URL!
    private var store: InboxStore!
    private var scope: String { InboxStore.scope(server: server, token: token) }

    override func setUp() async throws {
        previousServer = MeowShared.server; previousToken = MeowShared.deviceToken; previousDevice = MeowShared.deviceID
        MeowShared.server = server; MeowShared.deviceToken = token; MeowShared.deviceID = "dev_local"
        folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
    }
    override func tearDown() async throws {
        URLProtocol.unregisterClass(LocalInboxProtocol.self)
        LocalInboxProtocol.lock.withLock { LocalInboxProtocol.handler = nil }
        MeowShared.server = previousServer; MeowShared.deviceToken = previousToken; MeowShared.deviceID = previousDevice
        try? FileManager.default.removeItem(at: folder)
    }
    private func page(_ items: [MeowNotification], cursor: String? = nil) throws -> Data {
        let encoder = JSONEncoder(); encoder.dateEncodingStrategy = .iso8601; encoder.keyEncodingStrategy = .convertToSnakeCase
        return try encoder.encode(InboxPage(notifications: items, nextCursor: cursor))
    }

    func testSearchingAndFilteringSavedMessagesUseLocalProjectionAndMakeNoHTTPRequests() async throws {
        let items = [localItem("alpha", title: "Build completed"), localItem("match")]
        let data = try page(items)
        let calls = LocalCallCounter()
        LocalInboxProtocol.install { request in
            calls.increment()
            return request.url!.path.hasSuffix("/counts") ? (200, Data(#"{"unread":2,"pending":0}"#.utf8)) : (200, data)
        }
        let model = AppModel(inboxStore: store)
        await model.refresh()
        let baseline = calls.value
        model.search = "测试"
        await model.waitForInboxGrouping()
        XCTAssertEqual(model.notifications.map(\.id), ["match"], "search completes locally without HTTP/debounce")
        model.filter = .unread
        await model.waitForInboxGrouping()
        try await Task.sleep(for: .milliseconds(400))
        XCTAssertEqual(calls.value, baseline, "local search and filters must never issue HTTP")
    }

    func testOfflineRestartSearchesSavedHistoryBeyondFirstDisplayPage() async throws {
        let items = (0..<230).map { localItem("ntf_\($0)", title: $0 == 229 ? "历史测试" : "Other \($0)") }
        try await store.save(items, scope: scope)
        LocalInboxProtocol.install { _ in throw URLError(.notConnectedToInternet) }
        let model = AppModel(inboxStore: store)
        await model.refresh()
        model.search = "历史测试"
        await model.waitForInboxGrouping()
        XCTAssertEqual(model.notifications.map(\.id), ["ntf_229"])
        XCTAssertNotNil(model.loadError)
    }

    func testLocalMatchesAppearWhileHistorySyncIsStillPending() async throws {
        try await store.save([localItem("saved")], scope: scope)
        let started = expectation(description: "counts waiting")
        let gate = DispatchSemaphore(value: 0)
        let data = try page([localItem("saved")])
        LocalInboxProtocol.install { request in
            if request.url!.path == "/v1/inbox" {
                started.fulfill(); _ = gate.wait(timeout: .now() + 5)
                return (200, Data(#"{"unread":1,"pending":0}"#.utf8))
            }
            return (200, data)
        }
        let model = AppModel(inboxStore: store)
        let task = Task { await model.refresh() }
        await fulfillment(of: [started], timeout: 3)
        model.search = "测试"
        await model.waitForInboxGrouping()
        XCTAssertEqual(model.notifications.map(\.id), ["saved"])
        gate.signal(); await task.value
    }

    func testLaterPageFailureKeepsLastCompleteCacheAndNeverStoresPartialSnapshot() async throws {
        try await store.save([localItem("saved")], scope: scope)
        let data = try page([localItem("new")], cursor: "older")
        LocalInboxProtocol.install { request in
            if request.url!.path.hasSuffix("/counts") { return (200, Data(#"{"unread":1,"pending":0}"#.utf8)) }
            if request.url!.query!.contains("cursor") { throw URLError(.networkConnectionLost) }
            return (200, data)
        }
        let model = AppModel(inboxStore: store)
        await model.refresh()
        XCTAssertEqual(model.notifications.map(\.id), ["saved"])
        let restored = try await store.load(scope: scope)
        XCTAssertEqual(restored?.map(\.id), ["saved"])
        XCTAssertNotNil(model.loadError)
    }
}

private final class LocalCallCounter: @unchecked Sendable {
    private let lock = NSLock()
    private var count = 0
    var value: Int { lock.withLock { count } }
    func increment() { lock.withLock { count += 1 } }
}

extension InboxStoreTests {
    func testInvalidDatabaseIsReportedWithoutDeletingItsOriginalBytes() async throws {
        let file = database()
        try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true)
        let original = Data("not a SQLite database".utf8)
        try original.write(to: file)
        do { _ = try await InboxStore(fileURL: file).load(scope: "a"); XCTFail("corruption must be reported") }
        catch { XCTAssertFalse(error.localizedDescription.isEmpty) }
        XCTAssertEqual(try Data(contentsOf: file), original)
    }

    func testDirectoryCannotBeOpenedAsDatabase() async throws {
        let folder = database()
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        do { _ = try await InboxStore(fileURL: folder).load(scope: "a"); XCTFail("a directory is not a database") }
        catch { XCTAssertTrue(error.localizedDescription.contains("Couldn't open")) }
    }

    func testSQLiteFailureHalfwayThroughSnapshotRollsBackInsertsAndPruning() async throws {
        let file = database(), original = localItem("original")
        let store = InboxStore(fileURL: file)
        try await store.save([original], scope: "a")
        var connection: OpaquePointer?
        XCTAssertEqual(sqlite3_open(file.path, &connection), SQLITE_OK)
        defer { sqlite3_close(connection) }
        XCTAssertEqual(sqlite3_exec(connection, "CREATE TRIGGER fail_bad BEFORE INSERT ON inbox_messages WHEN NEW.id='bad' BEGIN SELECT RAISE(ABORT,'injected write failure'); END", nil, nil, nil), SQLITE_OK)
        do {
            try await store.save([localItem("new"), localItem("bad")], scope: "a")
            XCTFail("the injected SQLite failure must abort the transaction")
        } catch {}
        let restored = try await store.load(scope: "a")
        XCTAssertEqual(restored, [original])
    }

    func testUnwritableDatabaseLocationReportsFailure() async throws {
        let file = database()
        try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data("blocking file".utf8).write(to: file)
        let store = InboxStore(fileURL: file.appendingPathComponent("blocked.sqlite"))
        do { try await store.save([localItem("one")], scope: "a"); XCTFail("storage errors must be reported") }
        catch {}
    }
}

extension LocalInboxModelTests {
    func testPendingAndUnreadFiltersUseSavedQuestionStateWhileOffline() async throws {
        let open = localItem("open", read: true, question: .init(id: "q_open", status: .pending,
            expiresAt: .now.addingTimeInterval(3600)))
        let expired = localItem("expired", question: .init(id: "q_expired", status: .pending,
            expiresAt: .now.addingTimeInterval(-60)))
        let answered = localItem("answered", read: true, question: .init(id: "q_answered", status: .answered,
            answer: "done", expiresAt: .now.addingTimeInterval(3600)))
        try await store.save([open, expired, answered], scope: scope)
        LocalInboxProtocol.install { _ in throw URLError(.notConnectedToInternet) }
        let model = AppModel(inboxStore: store)
        await model.refresh()
        model.filter = .pending
        await model.waitForInboxGrouping()
        XCTAssertEqual(model.notifications.map(\.id), ["open"])
        model.filter = .unread
        await model.waitForInboxGrouping()
        XCTAssertEqual(model.notifications.map(\.id), ["expired"])
    }

    func testCancellingRateLimitWaitKeepsSavedHistory() async throws {
        try await store.save([localItem("saved")], scope: scope)
        let started = expectation(description: "rate limit returned")
        LocalInboxProtocol.install { request in
            if request.url!.path.hasSuffix("/counts") { return (200, Data(#"{"unread":1,"pending":0}"#.utf8)) }
            started.fulfill()
            return (429, Data(#"{"error":{"code":"rate_limited","message":"wait"}}"#.utf8))
        }
        let model = AppModel(inboxStore: store)
        let task = Task { await model.refresh() }
        await fulfillment(of: [started], timeout: 3)
        task.cancel()
        await task.value
        XCTAssertEqual(model.notifications.map(\.id), ["saved"])
        XCTAssertFalse(model.isLoading)
        XCTAssertNil(model.loadError)
    }

    func testRateLimitedHistoryPageWaitsThenResumesWithoutLosingTheSnapshot() async throws {
        let data = try page([localItem("after-rate-limit")])
        let calls = LocalCallCounter()
        LocalInboxProtocol.install { request in
            if request.url!.path.hasSuffix("/counts") { return (200, Data(#"{"unread":1,"pending":0}"#.utf8)) }
            calls.increment()
            return calls.value == 1 ? (429, Data(#"{"error":{"code":"rate_limited","message":"wait"}}"#.utf8)) : (200, data)
        }
        let model = AppModel(inboxStore: store)
        let started = ContinuousClock.now
        await model.refresh()
        XCTAssertEqual(model.notifications.map(\.id), ["after-rate-limit"])
        XCTAssertEqual(calls.value, 2)
        XCTAssertGreaterThanOrEqual(started.duration(to: .now), .seconds(1))
    }

    func testContinuousRateLimitingStopsAfterBoundedRetriesAndKeepsCache() async throws {
        try await store.save([localItem("saved")], scope: scope)
        let calls = LocalCallCounter()
        LocalInboxProtocol.install { request in
            if request.url!.path.hasSuffix("/counts") { return (200, Data(#"{"unread":1,"pending":0}"#.utf8)) }
            calls.increment()
            return (429, Data(#"{"error":{"code":"rate_limited","message":"wait"}}"#.utf8))
        }
        let model = AppModel(inboxStore: store)
        await model.refresh()
        XCTAssertEqual(calls.value, 4)
        XCTAssertEqual(model.notifications.map(\.id), ["saved"])
        XCTAssertNotNil(model.loadError)
    }

    private func executeSQLite(_ sql: String) throws {
        var connection: OpaquePointer?
        let path = folder.appendingPathComponent("inbox.sqlite").path
        XCTAssertEqual(sqlite3_open(path, &connection), SQLITE_OK)
        defer { sqlite3_close(connection) }
        guard sqlite3_exec(connection, sql, nil, nil, nil) == SQLITE_OK else {
            throw NSError(domain: "SQLiteTestSetup", code: 1)
        }
    }

    func testUnreadableSavedMessageReportsStorageFailureEvenWhenOffline() async throws {
        try await store.save([localItem("bad-cache")], scope: scope)
        try executeSQLite("UPDATE inbox_messages SET payload=X''")
        LocalInboxProtocol.install { _ in throw URLError(.notConnectedToInternet) }
        let model = AppModel(inboxStore: store)
        await model.refresh()
        XCTAssertNotNil(model.inboxStorageError, "the network error cannot hide an unreadable local database")
        XCTAssertNotNil(model.loadError)
    }

    func testFailedBackgroundCacheWriteIsVisibleWithoutLosingTheOnlineMessage() async throws {
        let initial = try page([localItem("existing")])
        let encoder = JSONEncoder(); encoder.dateEncodingStrategy = .iso8601; encoder.keyEncodingStrategy = .convertToSnakeCase
        let fresh = try encoder.encode(localItem("new"))
        LocalInboxProtocol.install { request in
            if request.url!.path.hasSuffix("/counts") { return (200, Data(#"{"unread":1,"pending":0}"#.utf8)) }
            return (200, request.url!.path.contains("/notifications/") ? fresh : initial)
        }
        let model = AppModel(inboxStore: store)
        await model.refresh()
        _ = try await store.load(scope: scope) // drain the initial save before installing the failure
        try executeSQLite("CREATE TRIGGER fail_new BEFORE INSERT ON inbox_messages WHEN NEW.id='new' BEGIN SELECT RAISE(ABORT,'disk write failed'); END")
        let found = await model.loadDetail("new")
        XCTAssertTrue(found)
        let deadline = Date.now.addingTimeInterval(3)
        while model.inboxStorageError == nil && Date.now < deadline { try await Task.sleep(for: .milliseconds(5)) }
        XCTAssertNotNil(model.inboxStorageError)
        XCTAssertNotNil(model.notification(id: "new"), "online content remains usable while the cache failure is reported")
    }

    func testReadingUnsavedOnlineContentCommitsThePayloadAndReadFlagTogether() async throws {
        let initial = try page([localItem("existing")])
        let encoder = JSONEncoder(); encoder.dateEncodingStrategy = .iso8601; encoder.keyEncodingStrategy = .convertToSnakeCase
        let fresh = try encoder.encode(localItem("new"))
        LocalInboxProtocol.install { request in
            XCTAssertFalse(request.url!.path.hasSuffix("/read"))
            return (200, request.url!.path.contains("/notifications/") ? fresh : initial)
        }
        let model = AppModel(inboxStore: store); await model.refresh()
        _ = try await store.load(scope: scope)
        try executeSQLite("CREATE TRIGGER fail_new BEFORE INSERT ON inbox_messages WHEN NEW.id='new' BEGIN SELECT RAISE(ABORT,'disk write failed'); END")
        let loaded = await model.loadDetail("new"); XCTAssertTrue(loaded)
        XCTAssertNotNil(model.inboxStorageError)
        try executeSQLite("DROP TRIGGER fail_new")
        let read = await model.markRead("new"); XCTAssertTrue(read)
        XCTAssertFalse(try XCTUnwrap(model.notification(id: "new")).isUnread)
        let durable = try await store.load(scope: scope)
        XCTAssertFalse(try XCTUnwrap(durable?.first(where: { $0.id == "new" })).isUnread)
        XCTAssertNil(model.inboxStorageError)
    }

    func testCacheDeletionFailureKeepsCurrentServerAndMessages() async throws {
        try await store.save([localItem("saved")], scope: scope)
        try executeSQLite("CREATE TRIGGER fail_delete BEFORE DELETE ON inbox_messages BEGIN SELECT RAISE(ABORT,'disk delete failed'); END")
        let queue = ReplyQueue(fileURL: folder.appendingPathComponent("replies.json")) { _ in throw URLError(.notConnectedToInternet) }
        let model = AppModel(replies: queue, inboxStore: store)
        let reset = await model.reset(server: URL(string: "https://new.invalid")!)
        XCTAssertFalse(reset)
        XCTAssertEqual(model.server, server)
        XCTAssertEqual(MeowShared.deviceToken, token)
        XCTAssertNotNil(model.alert)
        let saved = try await store.load(scope: scope)
        XCTAssertEqual(saved?.map(\.id), ["saved"])
    }

    func testOfflineDetailIsLoadedFromDisk() async throws {
        try await store.save([localItem("saved")], scope: scope)
        LocalInboxProtocol.install { _ in throw URLError(.notConnectedToInternet) }
        let model = AppModel(inboxStore: store)
        let found = await model.loadDetail("saved")
        XCTAssertTrue(found)
        XCTAssertEqual(model.notification(id: "saved")?.title, "测试通知")
    }

    func testOlderInflightHistoryCannotUndoCommittedLocalRead() async throws {
        let item = localItem("read-during-sync")
        try await store.save([item], scope: scope)
        let data = try page([item])
        let started = expectation(description: "history held"), gate = DispatchSemaphore(value: 0)
        defer { gate.signal() }
        LocalInboxProtocol.install { request in
            XCTAssertEqual(request.url!.path, "/v1/inbox")
            started.fulfill(); _ = gate.wait(timeout: .now() + 5)
            return (200, data)
        }
        let model = AppModel(inboxStore: store)
        let sync = Task { await model.refresh() }
        await fulfillment(of: [started], timeout: 3)
        let read = await model.markRead(item.id); XCTAssertTrue(read)
        gate.signal(); await sync.value
        XCTAssertEqual(model.counts.unread, 0)
        XCTAssertFalse(try XCTUnwrap(model.notification(id: item.id)).isUnread)
    }

    func testNewMessagesAndCountsRenderWithoutAnyCountRequest() async throws {
        let data = try page([localItem("fresh")])
        let calls = LocalCallCounter()
        LocalInboxProtocol.install { request in
            calls.increment(); XCTAssertEqual(request.url!.path, "/v1/inbox")
            return (200, data)
        }
        let model = AppModel(inboxStore: store)
        await model.refresh()
        XCTAssertEqual(model.notifications.map(\.id), ["fresh"])
        XCTAssertEqual(model.counts.unread, 1)
        XCTAssertFalse(model.isLoading)
        XCTAssertEqual(calls.value, 1)
    }

    func testAllPagesSyncIndependentlyOfSearchAndDisplayPaginationIsLocal() async throws {
        let first = try page((0..<200).map { localItem("id_\($0)", title: "Other \($0)") }, cursor: "next")
        let last = try page([localItem("target", title: "Second page 测试")])
        let calls = LocalCallCounter()
        LocalInboxProtocol.install { request in
            calls.increment()
            if request.url!.path.hasSuffix("/counts") { return (200, Data(#"{"unread":201,"pending":0}"#.utf8)) }
            let query = URLComponents(url: request.url!, resolvingAgainstBaseURL: false)!.queryItems!
            XCTAssertTrue(query.contains { $0.name == "filter" && $0.value == "all" })
            XCTAssertFalse(query.contains { $0.name == "q" })
            return (200, query.contains { $0.name == "cursor" } ? last : first)
        }
        let model = AppModel(inboxStore: store)
        model.search = "测试"
        await model.waitForInboxGrouping()
        await model.refresh()
        XCTAssertEqual(model.notifications.map(\.id), ["target"])
        let baseline = calls.value
        model.search = ""
        await model.waitForInboxGrouping()
        XCTAssertEqual(model.notifications.count, 50)
        XCTAssertNotNil(model.nextCursor)
        await model.loadMore()
        XCTAssertEqual(model.notifications.count, 100)
        XCTAssertEqual(calls.value, baseline)
        let cached = try await store.load(scope: scope)
        XCTAssertEqual(cached?.count, 201)
    }

    func testRepeatedCursorFailsWithoutReplacingSavedHistory() async throws {
        try await store.save([localItem("saved")], scope: scope)
        let data = try page([localItem("new")], cursor: "repeat")
        LocalInboxProtocol.install { request in
            request.url!.path.hasSuffix("/counts") ? (200, Data(#"{"unread":1,"pending":0}"#.utf8)) : (200, data)
        }
        let model = AppModel(inboxStore: store)
        await model.refresh()
        XCTAssertEqual(model.notifications.map(\.id), ["saved"])
        XCTAssertNotNil(model.loadError)
    }

    func testReadAndMarkAllReadArePersistedAndFilterOfflineWithoutRefresh() async throws {
        let data = try page([localItem("one"), localItem("two")])
        LocalInboxProtocol.install { request in
            if request.url!.path.hasSuffix("/counts") { return (200, Data(#"{"unread":2,"pending":0}"#.utf8)) }
            return request.httpMethod == "POST" ? (200, Data("{}".utf8)) : (200, data)
        }
        let model = AppModel(inboxStore: store)
        await model.refresh()
        model.filter = .unread
        await model.waitForInboxGrouping()
        await model.markRead("one")
        XCTAssertEqual(model.notifications.map(\.id), ["two"])
        let oneRead = try await store.load(scope: scope)
        XCTAssertEqual(oneRead?.filter(\.isUnread).map(\.id), ["two"])
        await model.markAllRead()
        XCTAssertTrue(model.notifications.isEmpty)
        LocalInboxProtocol.install { _ in throw URLError(.notConnectedToInternet) }
        let restarted = AppModel(inboxStore: store)
        await restarted.refresh()
        restarted.filter = .unread
        await restarted.waitForInboxGrouping()
        XCTAssertTrue(restarted.notifications.isEmpty)
        restarted.filter = .all
        await restarted.waitForInboxGrouping()
        XCTAssertEqual(restarted.notifications.count, 2)
    }

    func testLocalSearchMatchesSessionWorkspaceAndIdentityWithoutHTTP() async throws {
        var n = localItem("session-message", title: "Result")
        n.context = .init(workspace: "项目甲", workspaceId: "project-key", session: "部署审查", sessionId: "thread-123")
        try await store.save([n, localItem("other", title: "Different")], scope: scope)
        let calls = LocalCallCounter()
        LocalInboxProtocol.install { _ in calls.increment(); throw URLError(.notConnectedToInternet) }
        let model = AppModel(inboxStore: store)
        await model.refresh()
        let before = calls.value
        for query in ["部署审查", "项目甲", "thread-123", "project-key"] {
            model.search = query
            await model.waitForInboxGrouping()
            XCTAssertEqual(model.notifications.map(\.id), ["session-message"])
        }
        XCTAssertEqual(calls.value, before)
    }

    func testLocalSearchTreatsUnicodePunctuationAndSQLSyntaxLiterally() async throws {
        var first = localItem("first", title: "测试 CAFÉ 😺")
        first.body = "100%_ and ' OR 1=1 --"
        var second = localItem("second", title: "Other")
        second.body = "Plain"; second.source = "Different"
        try await store.save([first, second], scope: scope)
        LocalInboxProtocol.install { _ in throw URLError(.notConnectedToInternet) }
        let model = AppModel(inboxStore: store)
        await model.refresh()
        for query in ["测试", "café", "😺", "%_", "' OR 1=1 --"] {
            model.search = query
            await model.waitForInboxGrouping()
            XCTAssertEqual(model.notifications.map(\.id), ["first"], "query: \(query)")
        }
        model.search = "Different"
        await model.waitForInboxGrouping()
        XCTAssertEqual(model.notifications.map(\.id), ["second"])
    }

    func testAcceptedAnswerSurvivesAnOlderInflightSyncAndPersists() async throws {
        let original = localItem("question", question: .init(id: "q_question", status: .pending,
            choices: ["approve", "reject"], expiresAt: .now.addingTimeInterval(3600)))
        try await store.save([original], scope: scope)
        let data = try page([original])
        let started = expectation(description: "old snapshot held"), gate = DispatchSemaphore(value: 0)
        LocalInboxProtocol.install { request in
            if request.url!.path.hasSuffix("/counts") { return (200, Data(#"{"unread":0,"pending":0}"#.utf8)) }
            started.fulfill(); _ = gate.wait(timeout: .now() + 5)
            return (200, data)
        }
        let queue = ReplyQueue(fileURL: folder.appendingPathComponent("replies.json")) { _ in
            MeowQuestion(id: "q_question", status: .answered, question: original.title,
                choices: ["approve", "reject"], answer: "approve", createdAt: original.createdAt,
                expiresAt: original.question!.expiresAt, resolvedAt: .now, notificationId: original.id)
        }
        let model = AppModel(replies: queue, inboxStore: store)
        let task = Task { await model.refresh() }
        await fulfillment(of: [started], timeout: 3)
        let answer = await model.answer("q_question", with: "approve")
        XCTAssertEqual(answer, .sent)
        gate.signal(); await task.value
        XCTAssertEqual(model.notification(id: "question")?.question?.status, .answered)
        let saved = try await store.load(scope: scope)
        XCTAssertEqual(saved?.first?.question?.answer, "approve")
    }

    func testCache404AndOlderSyncBothPreserveSavedHistory() async throws {
        try await store.save([localItem("removed")], scope: scope)
        let data = try page([localItem("removed")])
        let started = expectation(description: "sync held"), gate = DispatchSemaphore(value: 0)
        LocalInboxProtocol.install { request in
            if request.url!.path.hasSuffix("/counts") { return (200, Data(#"{"unread":0,"pending":0}"#.utf8)) }
            if request.url!.path.contains("/notifications/") { return (404, Data(#"{"error":{"code":"not_found","message":"removed"}}"#.utf8)) }
            started.fulfill(); _ = gate.wait(timeout: .now() + 5)
            return (200, data)
        }
        let model = AppModel(inboxStore: store)
        let task = Task { await model.refresh() }
        await fulfillment(of: [started], timeout: 3)
        let found = await model.loadDetail("removed")
        XCTAssertTrue(found)
        gate.signal(); await task.value
        XCTAssertNotNil(model.notification(id: "removed"))
        let saved = try await store.load(scope: scope)
        XCTAssertEqual(saved?.map(\.id), ["removed"])
    }

    func testResetErasesOldScopeAndLateSyncCannotWriteItBack() async throws {
        let oldScope = scope
        try await store.save([localItem("old")], scope: oldScope)
        let data = try page([localItem("old")])
        let started = expectation(description: "old sync held"), gate = DispatchSemaphore(value: 0)
        LocalInboxProtocol.install { request in
            if request.url!.path.hasSuffix("/counts") { return (200, Data(#"{"unread":1,"pending":0}"#.utf8)) }
            started.fulfill(); _ = gate.wait(timeout: .now() + 5)
            return (200, data)
        }
        let queue = ReplyQueue(fileURL: folder.appendingPathComponent("replies.json")) { _ in throw URLError(.notConnectedToInternet) }
        let model = AppModel(replies: queue, inboxStore: store)
        let task = Task { await model.refresh() }
        await fulfillment(of: [started], timeout: 3)
        let reset = await model.reset(server: URL(string: "https://new.invalid")!)
        XCTAssertTrue(reset)
        gate.signal(); await task.value
        XCTAssertTrue(model.notifications.isEmpty)
        let saved = try await store.load(scope: oldScope)
        XCTAssertNil(saved)
    }
}
