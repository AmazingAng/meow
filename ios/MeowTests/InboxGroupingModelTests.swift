import XCTest
@testable import Meow

private final class GroupingProtocol: URLProtocol, @unchecked Sendable {
    static let lock = NSLock()
    nonisolated(unsafe) static var handler: ((URLRequest) throws -> (Int, Data))?
    static func install(_ handler: @escaping (URLRequest) throws -> (Int, Data)) {
        lock.withLock { Self.handler = handler }; URLProtocol.registerClass(Self.self)
    }
    override class func canInit(with request: URLRequest) -> Bool { request.url?.host == "grouping.invalid" }
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
final class InboxGroupingModelTests: XCTestCase {
    private let server = URL(string: "https://grouping.invalid")!, token = "mdt_grouping"
    private var oldServer: URL?, oldToken: String?, oldDevice: String?
    private var folder: URL!, store: InboxStore!, preferences: InboxGroupingStore!
    private var suite: String!, defaults: UserDefaults!
    private var scope: String { InboxStore.scope(server: server, token: token) }
    override func setUp() async throws {
        oldServer = MeowShared.server; oldToken = MeowShared.deviceToken; oldDevice = MeowShared.deviceID
        MeowShared.server = server; MeowShared.deviceToken = token; MeowShared.deviceID = "grouping-device"
        folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
        suite = "grouping-tests-\(UUID().uuidString)"; defaults = UserDefaults(suiteName: suite)!
        preferences = InboxGroupingStore(defaults: defaults)
        GroupingProtocol.install { request in
            (200, Data((request.url!.path.hasSuffix("/counts") ? #"{"unread":0,"pending":0}"# : #"{"notifications":[]}"#).utf8))
        }
    }
    override func tearDown() async throws {
        URLProtocol.unregisterClass(GroupingProtocol.self)
        GroupingProtocol.lock.withLock { GroupingProtocol.handler = nil }
        MeowShared.server = oldServer; MeowShared.deviceToken = oldToken; MeowShared.deviceID = oldDevice
        defaults.removePersistentDomain(forName: suite)
        try? FileManager.default.removeItem(at: folder)
    }
    private func item(_ id: String, session: String = "Review", body: String = "Result") -> MeowNotification {
        MeowNotification(id: id, type: .info, title: "New result", body: body, source: "Codex", priority: "normal",
            pushStatus: "accepted", createdAt: Date(timeIntervalSince1970: 1_790_000_000),
            context: NotificationOrigin(workspace: "meow", workspaceId: "w", session: session), event: .result)
    }
    private func model() -> AppModel {
        AppModel(replies: ReplyQueue(fileURL: folder.appendingPathComponent("replies.json")) { _ in throw URLError(.unsupportedURL) },
                 inboxStore: store, groupingStore: preferences)
    }

    func testGroupPagingSearchAndCountsCoverEntireLocalHistoryWithoutHTTP() async throws {
        try await store.save((0..<120).map { item("n\($0)", session: "Session \($0)") }, scope: scope)
        let model = model(); await model.refresh()
        model.inboxMode = .sessions; await model.waitForInboxGrouping()
        XCTAssertEqual(model.visibleInboxGroups.count, 50)
        XCTAssertEqual(model.allInboxGroups.count, 120)
        model.loadMoreInboxGroups(); XCTAssertEqual(model.visibleInboxGroups.count, 100)
        let calls = Counter()
        GroupingProtocol.install { _ in calls.increment(); throw URLError(.notConnectedToInternet) }
        model.search = "Session 119"; await model.waitForInboxGrouping()
        XCTAssertEqual(model.matchingInboxGroups.count, 1)
        XCTAssertEqual(model.matchingInboxGroups.first?.messages.first?.id, "n119")
        XCTAssertEqual(calls.value, 0)
    }

    func testMergeAgainUnmergeAndRestartRetainHistoryAndIndependentRules() async throws {
        try await store.save([item("a", session: "A"), item("b", session: "B"), item("c", session: "C")], scope: scope)
        let model = model(); await model.refresh(); model.inboxMode = .sessions; await model.waitForInboxGrouping()
        model.mergeInboxGroups(Set(model.allInboxGroups.prefix(2).map(\.id))); await model.waitForInboxGrouping()
        XCTAssertEqual(model.allInboxGroups.count, 2)
        model.mergeInboxGroups(Set(model.allInboxGroups.map(\.id))); await model.waitForInboxGrouping()
        XCTAssertEqual(model.allInboxGroups.count, 1)
        XCTAssertEqual(model.allInboxGroups.first?.members.count, 3)
        model.inboxMode = .sources; await model.waitForInboxGrouping()
        XCTAssertNil(model.allInboxGroups.first?.mergeID)
        model.inboxMode = .sessions; await model.waitForInboxGrouping()
        let restarted = self.model(); await restarted.refresh(); await restarted.waitForInboxGrouping()
        XCTAssertEqual(restarted.inboxMode, .sessions)
        XCTAssertEqual(restarted.allInboxGroups.count, 1, "server cache omission must retain the complete local merged history")
        restarted.unmergeInboxGroup(try XCTUnwrap(restarted.allInboxGroups.first)); await restarted.waitForInboxGrouping()
        XCTAssertEqual(restarted.allInboxGroups.count, 3)
        XCTAssertEqual(restarted.savedNotifications.count, 3)
    }

    func testMatchingReadSnapshotsIncludeUndisplayedMessagesAndPartialFailuresRollBack() async throws {
        let items = (0..<75).map { item("n\($0)", body: $0 == 74 ? "unmatched" : "needle") }
        try await store.save(items, scope: scope)
        let model = model(); await model.refresh(); model.inboxMode = .sessions
        model.search = "needle"; await model.waitForInboxGrouping()
        let ids = Set(try XCTUnwrap(model.matchingInboxGroups.first).matches.map(\.id))
        XCTAssertEqual(ids.count, 74)
        let calls = Counter()
        GroupingProtocol.install { request in
            calls.increment()
            if request.url!.path.contains("/n2/") { return (503, Data(#"{"error":{"code":"unavailable","message":"retry"}}"#.utf8)) }
            if request.url!.path.contains("/n3/") { return (404, Data(#"{"error":{"code":"not_found","message":"expired cache"}}"#.utf8)) }
            return (200, Data("{}".utf8))
        }
        await model.markMatchingRead(ids)
        XCTAssertEqual(calls.value, 74)
        XCTAssertEqual(model.savedNotifications.filter(\.isUnread).count, 2)
        XCTAssertEqual(model.failedGroupReadIDs, ["n2"])
        XCTAssertNotNil(model.toast)
        XCTAssertFalse(try XCTUnwrap(model.notification(id: "n3")).isUnread)
        XCTAssertTrue(try XCTUnwrap(model.notification(id: "n74")).isUnread)
        GroupingProtocol.install { _ in (200, Data("{}".utf8)) }
        await model.markMatchingRead(model.failedGroupReadIDs)
        XCTAssertTrue(model.failedGroupReadIDs.isEmpty)
        XCTAssertEqual(model.savedNotifications.filter(\.isUnread).map(\.id), ["n74"])
    }

    func testNewMessagesDuringMatchingReadRemainUnread() async throws {
        let initial = (0..<3).map { item("n\($0)") }
        try await store.save(initial, scope: scope)
        let model = model(); await model.refresh()
        let started = expectation(description: "three reads in flight"); started.expectedFulfillmentCount = 3
        let gate = DispatchSemaphore(value: 0), calls = Counter()
        let encoder = JSONEncoder(); encoder.dateEncodingStrategy = .iso8601; encoder.keyEncodingStrategy = .convertToSnakeCase
        let page = try encoder.encode(InboxPage(notifications: initial + [item("incoming")]))
        GroupingProtocol.install { request in
            if request.url!.path.hasSuffix("/read") {
                calls.increment(); started.fulfill(); _ = gate.wait(timeout: .now() + 10)
                return (200, Data("{}".utf8))
            }
            return (200, request.url!.path.hasSuffix("/counts") ? Data(#"{"unread":4,"pending":0}"#.utf8) : page)
        }
        let operation = Task { await model.markMatchingRead(Set(initial.map(\.id))) }
        await fulfillment(of: [started], timeout: 5)
        await model.refresh()
        for _ in 0..<3 { gate.signal() }
        await operation.value
        XCTAssertEqual(calls.value, 3)
        XCTAssertTrue(try XCTUnwrap(model.notification(id: "incoming")).isUnread)
        XCTAssertEqual(model.savedNotifications.filter(\.isUnread).map(\.id), ["incoming"])
    }

    func testServerResetRejectsLateBatchReadFailures() async throws {
        let initial = (0..<3).map { item("n\($0)") }
        try await store.save(initial, scope: scope)
        let model = model(); await model.refresh()
        let started = expectation(description: "three reads in flight"); started.expectedFulfillmentCount = 3
        let gate = DispatchSemaphore(value: 0)
        GroupingProtocol.install { _ in
            started.fulfill(); _ = gate.wait(timeout: .now() + 10)
            return (503, Data(#"{"error":{"code":"unavailable","message":"late failure"}}"#.utf8))
        }
        let operation = Task { await model.markMatchingRead(Set(initial.map(\.id))) }
        await fulfillment(of: [started], timeout: 5)
        let reset = await model.reset(server: URL(string: "https://other.invalid")!)
        for _ in 0..<3 { gate.signal() }
        await operation.value
        XCTAssertTrue(reset)
        XCTAssertTrue(model.savedNotifications.isEmpty)
        XCTAssertEqual(model.counts.unread, 0)
        XCTAssertTrue(model.failedGroupReadIDs.isEmpty)
        XCTAssertFalse(model.isMarkingGroupRead)
        XCTAssertNil(model.toast)
    }

    func testRapidQueryChangesAndServerResetDiscardOldGroupingWork() async throws {
        try await store.save((0..<3000).map { item("n\($0)", session: "Session \($0)") }, scope: scope)
        let model = model(); await model.refresh(); model.inboxMode = .sessions
        model.search = "Session 1"; model.search = "Session 2999"
        await model.waitForInboxGrouping()
        XCTAssertEqual(model.matchingInboxGroups.first?.title, "Session 2999")
        model.search = ""
        let reset = await model.reset(server: URL(string: "https://other.invalid")!)
        XCTAssertTrue(reset)
        await model.waitForInboxGrouping()
        XCTAssertTrue(model.allInboxGroups.isEmpty)
        XCTAssertTrue(model.inboxPath.isEmpty)
        XCTAssertEqual(model.inboxMode, .sessions)
        XCTAssertTrue(preferences.load(scope: scope).isEmpty)
    }

    func testGlobalReadCannotBeSubmittedTwiceOrReportFailureInAnotherServer() async throws {
        try await store.save([item("n")], scope: scope)
        let model = model(); await model.refresh()
        let started = expectation(description: "global read in flight")
        let gate = DispatchSemaphore(value: 0), calls = Counter()
        GroupingProtocol.install { request in
            XCTAssertEqual(request.url!.path, "/v1/inbox/read")
            calls.increment(); started.fulfill(); _ = gate.wait(timeout: .now() + 10)
            return (503, Data(#"{"error":{"code":"unavailable","message":"old server failure"}}"#.utf8))
        }
        let operation = Task { await model.markAllRead() }
        await fulfillment(of: [started], timeout: 5)
        XCTAssertTrue(model.isMarkingAllRead)
        await model.markAllRead()
        await model.markMatchingRead(["n"])
        XCTAssertEqual(calls.value, 1)
        let reset = await model.reset(server: URL(string: "https://other.invalid")!)
        gate.signal(); await operation.value
        XCTAssertTrue(reset)
        XCTAssertFalse(model.isMarkingAllRead)
        XCTAssertNil(model.alert)
        XCTAssertTrue(model.savedNotifications.isEmpty)
    }
}

private final class Counter: @unchecked Sendable {
    private let lock = NSLock()
    private var count = 0
    var value: Int { lock.withLock { count } }
    func increment() { lock.withLock { count += 1 } }
}
