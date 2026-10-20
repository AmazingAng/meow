import XCTest
@testable import Meow

final class InboxDeletionRangeTests: XCTestCase {
    func testCalendarDaysUseLocalMidnightAcrossDaylightSaving() throws {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = try XCTUnwrap(TimeZone(identifier: "America/New_York"))
        let now = try XCTUnwrap(MeowJSON.parseDate("2026-03-08T20:00:00Z"))
        let today = InboxDeleteRange.today.interval(now: now, calendar: calendar)
        XCTAssertEqual(today.from, 1_772_946_000_000) // 2026-03-08 00:00 EST
        let both = InboxDeleteRange.todayAndYesterday.interval(now: now, calendar: calendar)
        XCTAssertEqual(both.from, today.from - 86_400_000)
        XCTAssertEqual(today.before, Int64(now.timeIntervalSince1970 * 1000) + 1)
        XCTAssertEqual(InboxDeleteRange.beforeToday.interval(now: now, calendar: calendar).before, today.from)
        XCTAssertEqual(InboxDeleteRange.beforeYesterday.interval(now: now, calendar: calendar).before, both.from)
    }

    func testCalendarMonthClampsAtMonthEnd() throws {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(secondsFromGMT: 0)!
        let now = try XCTUnwrap(MeowJSON.parseDate("2026-03-31T12:30:00Z"))
        let expected = try XCTUnwrap(MeowJSON.parseDate("2026-02-28T12:30:00Z"))
        XCTAssertEqual(InboxDeleteRange.lastMonth.interval(now: now, calendar: calendar).from,
                       Int64(expected.timeIntervalSince1970 * 1000))
        XCTAssertEqual(Set(InboxDeleteRange.allCases.map(\.title)).count, InboxDeleteRange.allCases.count)
        XCTAssertEqual(InboxDeleteRange.allCases.map(\.id), InboxDeleteRange.allCases)
    }

    func testGeneratedRangesAreHalfOpenAndRecentOlderPartitionHistory() {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(secondsFromGMT: 28_800)!
        for index in 0..<120 {
            let now = Date(timeIntervalSince1970: 1_790_000_000 + Double(index * 86_431))
            let all = InboxDeleteRange.all.interval(now: now, calendar: calendar)
            XCTAssertEqual(all.from, 0)
            XCTAssertTrue(all.contains(now))
            XCTAssertFalse(all.contains(Date(timeIntervalSince1970: Double(all.before) / 1000)))
            for (recent, older) in [(InboxDeleteRange.lastHour, InboxDeleteRange.beforeOneHour), (.today, .beforeToday),
                                     (.todayAndYesterday, .beforeYesterday), (.lastMonth, .beforeOneMonth)] {
                let r = recent.interval(now: now, calendar: calendar)
                let o = older.interval(now: now, calendar: calendar)
                XCTAssertEqual(r.from, o.before)
                XCTAssertEqual(r.before, all.before)
                XCTAssertGreaterThan(r.before, r.from)
                XCTAssertEqual(o.from, 0)
                let boundary = Date(timeIntervalSince1970: Double(r.from) / 1000)
                XCTAssertTrue(r.contains(boundary))
                XCTAssertFalse(o.contains(boundary))
            }
        }
    }
}

private final class DeletionProtocol: URLProtocol, @unchecked Sendable {
    static let lock = NSLock()
    static var handler: ((URLRequest) throws -> (Int, Data))?
    static func install(_ handler: @escaping (URLRequest) throws -> (Int, Data)) {
        lock.withLock { Self.handler = handler }
        URLProtocol.registerClass(Self.self)
    }
    override class func canInit(with request: URLRequest) -> Bool { request.url?.host == "delete.invalid" }
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

private final class DeletionGate: @unchecked Sendable {
    let lock = NSLock()
    let release = DispatchSemaphore(value: 0)
    private var started = false
    func hold() throws {
        lock.withLock { started = true }
        guard release.wait(timeout: .now() + 8) == .success else { throw URLError(.timedOut) }
    }
    var isStarted: Bool { lock.withLock { started } }
}

@MainActor
final class InboxDeletionModelTests: XCTestCase {
    let server = URL(string: "https://delete.invalid")!
    let token = "mdt_deletion_fixture"
    var previousServer: URL?
    var previousToken: String?
    var previousDevice: String?
    var previousKey: String?
    var previousKeyID: String?
    var folder: URL!
    var store: InboxStore!
    var scope: String { InboxStore.scope(server: server, token: token) }
    override func setUp() async throws {
        previousServer = MeowShared.server; previousToken = MeowShared.deviceToken; previousDevice = MeowShared.deviceID
        previousKey = Keychain.get("device_key"); previousKeyID = MeowShared.defaults.string(forKey: "device_key_id")
        MeowShared.server = server; MeowShared.deviceToken = token; MeowShared.deviceID = "dev_delete"
        folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
    }
    override func tearDown() async throws {
        URLProtocol.unregisterClass(DeletionProtocol.self)
        DeletionProtocol.lock.withLock { DeletionProtocol.handler = nil }
        MeowShared.server = previousServer; MeowShared.deviceToken = previousToken; MeowShared.deviceID = previousDevice
        Keychain.set(previousKey, for: "device_key"); MeowShared.defaults.set(previousKeyID, forKey: "device_key_id")
        try? FileManager.default.removeItem(at: folder)
    }
    func item(_ id: String, milliseconds: Int64 = 1000, read: Bool = false) -> MeowNotification {
        MeowNotification(id: id, type: .info, title: id, body: "test", source: "Codex", priority: "normal",
            pushStatus: "accepted", createdAt: Date(timeIntervalSince1970: Double(milliseconds)/1000), readAt: read ? .now : nil)
    }
    func page(_ items: [MeowNotification]) throws -> Data {
        let encoder = JSONEncoder(); encoder.dateEncodingStrategy = .iso8601; encoder.keyEncodingStrategy = .convertToSnakeCase
        return try encoder.encode(InboxPage(notifications: items))
    }
    func model(_ items: [MeowNotification]) async throws -> AppModel {
        let data = try page(items)
        DeletionProtocol.install { request in
            (200, request.url!.path.hasSuffix("/counts") ? Data("{\"unread\":\(items.filter(\.isUnread).count),\"pending\":0}".utf8) : data)
        }
        let model = AppModel(inboxStore: store)
        await model.refresh()
        XCTAssertEqual(model.savedNotifications.count, items.count)
        return model
    }
    private func wait(_ gate: DeletionGate) async throws {
        for _ in 0..<200 {
            if gate.isStarted { return }
            try await Task.sleep(for: .milliseconds(5))
        }
        XCTFail("request never reached held boundary")
    }

    func testConfirmedDeletionPersistsAndDoesNotReturnAfterRestartOrRefresh() async throws {
        let model = try await model([item("one"), item("two", read: true)])
        let remaining = try page([item("two", read: true)])
        DeletionProtocol.install { request in
            if request.httpMethod == "DELETE" {
                XCTAssertEqual(request.url!.path, "/v1/notifications/one")
                XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer mdt_deletion_fixture")
                return (204, Data())
            }
            return (200, request.url!.path.hasSuffix("/counts") ? Data(#"{"unread":0,"pending":0}"#.utf8) : remaining)
        }
        await model.deleteNotification("one")
        XCTAssertNil(model.notification(id: "one"))
        XCTAssertEqual(model.counts.unread, 0)
        let saved = try await store.load(scope: scope)
        XCTAssertEqual(saved?.map(\.id), ["two"])
        let restored = AppModel(inboxStore: store)
        await restored.refresh()
        XCTAssertEqual(restored.savedNotifications.map(\.id), ["two"])
    }

    func testFailuresAndOldServer404RetainHistoryAndShowError() async throws {
        let model = try await model([item("one")])
        for status in [401, 404, 429, 500] {
            DeletionProtocol.install { _ in (status, Data(#"{"error":{"message":"delete failed"}}"#.utf8)) }
            await model.deleteNotification("one")
            XCTAssertNotNil(model.notification(id: "one"))
            XCTAssertNotNil(model.alert)
            XCTAssertEqual(model.counts.unread, 1)
        }
        DeletionProtocol.install { _ in throw URLError(.notConnectedToInternet) }
        await model.deleteNotifications(in: .init(from: 0, before: 2000))
        XCTAssertNotNil(model.notification(id: "one"))
        let saved = try await store.load(scope: scope)
        XCTAssertEqual(saved?.count, 1)
    }

    func testBulkDeletionIgnoresSearchAndFilterButKeepsUpperBoundary() async throws {
        let model = try await model([item("shown"), item("hidden", read: true), item("new", milliseconds: 2000)])
        model.search = "shown"; model.filter = .unread
        DeletionProtocol.install { request in
            XCTAssertEqual(request.httpMethod, "DELETE")
            XCTAssertEqual(request.url!.path, "/v1/inbox")
            let query = URLComponents(url: request.url!, resolvingAgainstBaseURL: false)!.queryItems!
            XCTAssertEqual(Set(query.map(\.name)), ["from", "before"])
            XCTAssertEqual(query.first { $0.name == "from" }?.value, "0")
            XCTAssertEqual(query.first { $0.name == "before" }?.value, "2000")
            return (200, Data(#"{"deleted":2}"#.utf8))
        }
        await model.deleteNotifications(in: .init(from: 0, before: 2000))
        model.search = ""; model.filter = .all
        XCTAssertEqual(model.savedNotifications.map(\.id), ["new"])
        XCTAssertEqual(model.counts.unread, 1)
    }

    func testStaleRefreshAndDetailCannotResurrectDeletedMessage() async throws {
        for detail in [false, true] {
            let model = try await model([item("one")])
            let oldPage = try page([item("one")])
            let encoder = JSONEncoder(); encoder.dateEncodingStrategy = .iso8601; encoder.keyEncodingStrategy = .convertToSnakeCase
            let oldDetail = try encoder.encode(item("one"))
            let gate = DeletionGate()
            DeletionProtocol.install { request in
                if request.httpMethod == "DELETE" { return (204, Data()) }
                if request.url!.path.hasSuffix("/counts") { return (200, Data(#"{"unread":1,"pending":0}"#.utf8)) }
                try gate.hold()
                return (200, detail ? oldDetail : oldPage)
            }
            let loading = Task { if detail { _ = await model.loadDetail("one") } else { await model.refresh() } }
            try await wait(gate)
            await model.deleteNotification("one")
            gate.release.signal()
            await loading.value
            XCTAssertNil(model.notification(id: "one"), "stale \(detail ? "detail" : "refresh") resurrected deleted record")
            XCTAssertEqual(model.counts.unread, 0)
        }
    }

    func testDelayedDeleteCannotRemoveNewServerSession() async throws {
        let model = try await model([item("one")])
        let gate = DeletionGate()
        DeletionProtocol.install { _ in try gate.hold(); return (204, Data()) }
        let deleting = Task { await model.deleteNotification("one") }
        try await wait(gate)
        let newServer = URL(string: "https://delete.invalid/new")!
        let switched = await model.reset(server: newServer)
        XCTAssertTrue(switched)
        let newPage = try page([item("one", milliseconds: 5000)])
        let registration = Data(#"{"device":{"id":"dev_new","name":"New","apns_env":"production","has_apns_token":false,"created_at":"2026-10-21T00:00:00Z","last_seen_at":"2026-10-21T00:00:00Z"},"device_token":"mdt_new","key":{"id":"key_new","name":"Default","hint":"new","scopes":["notify"],"created_at":"2026-10-21T00:00:00Z","secret":"meow_new"}}"#.utf8)
        DeletionProtocol.install { request in
            if request.httpMethod == "POST" { return (201, registration) }
            return (200, request.url!.path.hasSuffix("/counts") ? Data(#"{"unread":1,"pending":0}"#.utf8) : newPage)
        }
        await model.register()
        XCTAssertTrue(model.isRegistered)
        await model.refresh()
        XCTAssertEqual(model.savedNotifications.map(\.id), ["one"])
        gate.release.signal()
        await deleting.value
        XCTAssertEqual(model.server, newServer)
        XCTAssertEqual(model.savedNotifications.map(\.id), ["one"], "old delete must not erase new server's same-ID record")
        XCTAssertEqual(model.counts.unread, 1)
        XCTAssertNil(model.alert)
    }
}


extension InboxDeletionModelTests {
    func testStaleCountsCannotRestoreUnreadBadgeAfterDeletion() async throws {
        let model = try await model([item("one")])
        let gate = DeletionGate()
        DeletionProtocol.install { request in
            if request.httpMethod == "DELETE" { return (204, Data()) }
            try gate.hold()
            return (200, Data(#"{"unread":1,"pending":0}"#.utf8))
        }
        let counts = Task { await model.refreshCounts() }
        try await wait(gate)
        await model.deleteNotification("one")
        gate.release.signal()
        await counts.value
        XCTAssertEqual(model.counts.unread, 0)
        let saved = try await store.load(scope: scope)
        XCTAssertEqual(saved, [])
    }

    func testConcurrentDeleteDoesNotSendDuplicateRequests() async throws {
        let model = try await model([item("one")])
        let gate = DeletionGate()
        DeletionProtocol.install { _ in try gate.hold(); return (204, Data()) }
        let first = Task { await model.deleteNotification("one") }
        try await wait(gate)
        XCTAssertTrue(model.isDeletingNotifications)
        await model.deleteNotification("one")
        gate.release.signal()
        await first.value
        XCTAssertFalse(model.isDeletingNotifications)
        XCTAssertTrue(model.savedNotifications.isEmpty)
    }

    func testDetailNotInSnapshotCannotAppearAfterBulkDeletion() async throws {
        let model = try await model([])
        let encoder = JSONEncoder(); encoder.dateEncodingStrategy = .iso8601; encoder.keyEncodingStrategy = .convertToSnakeCase
        let oldDetail = try encoder.encode(item("unknown"))
        let gate = DeletionGate()
        DeletionProtocol.install { request in
            if request.httpMethod == "DELETE" { return (200, Data(#"{"deleted":1}"#.utf8)) }
            try gate.hold()
            return (200, oldDetail)
        }
        let loading = Task { await model.loadDetail("unknown") }
        try await wait(gate)
        await model.deleteNotifications(in: .init(from: 0, before: 2000))
        gate.release.signal()
        let found = await loading.value
        XCTAssertFalse(found)
        XCTAssertNil(model.notification(id: "unknown"))
    }
}
