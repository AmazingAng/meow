import SQLite3
import UserNotifications
import XCTest

@testable import Meow

final class LocalUnreadStoreTests: XCTestCase {
    private func item(_ id: String, read: Bool = false) -> MeowNotification {
        MeowNotification(
            id: id, type: .info, title: id, body: "完整正文", source: "fixture", priority: "normal",
            pushStatus: "accepted", createdAt: .now, readAt: read ? .now : nil)
    }
    func testLegacyMigrationPreservesReadStateAndDoesNotImportTwice() async throws {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let oldURL = folder.appendingPathComponent("legacy.sqlite")
        let newURL = folder.appendingPathComponent("shared.sqlite")
        let old = InboxStore(fileURL: oldURL)
        try await old.save([item("read", read: true), item("unread")], scope: "scope")
        let shared = InboxStore(fileURL: newURL, legacyURL: oldURL)
        try await shared.prepareForApp()
        let imported = try await shared.load(scope: "scope")
        XCTAssertEqual(imported?.count, 2)
        XCTAssertFalse(try XCTUnwrap(imported?.first { $0.id == "read" }).isUnread)
        XCTAssertTrue(FileManager.default.fileExists(atPath: oldURL.path))
        try await shared.applyChanges([], deleting: ["unread"], scope: "scope")
        try await old.save([item("read", read: true), item("unread"), item("later-old-row")], scope: "scope")
        try await shared.prepareForApp()
        let next = try await shared.load(scope: "scope")
        XCTAssertEqual(next?.map(\.id), ["read"])
    }
    func testTwoConnectionsDeduplicateAndPreserveLocalReadOverRemoteUpdates() async throws {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let file = folder.appendingPathComponent("shared.sqlite")
        let app = InboxStore(fileURL: file)
        let extensionStore = InboxStore(fileURL: file)
        try await app.activate(scope: "scope")
        try await extensionStore.receiveVerified(item("one", read: true), scope: "scope")
        var count = try await app.unreadCount(scope: "scope")
        XCTAssertEqual(count, 1, "server read state cannot mark a new local message read")
        try await app.markLocalRead(["one"], scope: "scope")
        try await extensionStore.receiveVerified(item("one"), scope: "scope")
        count = try await extensionStore.unreadCount(scope: "scope")
        XCTAssertEqual(count, 0)
        let saved = try await app.load(scope: "scope")
        XCTAssertEqual(saved?.count, 1)
        XCTAssertFalse(try XCTUnwrap(saved?.first).isUnread)
    }
    func testDeletedAndOldRegistrationReceiptsCannotResurrectMessages() async throws {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let file = folder.appendingPathComponent("shared.sqlite")
        let app = InboxStore(fileURL: file)
        let extensionStore = InboxStore(fileURL: file)
        try await app.activate(scope: "old")
        try await extensionStore.receiveVerified(item("gone"), scope: "old")
        try await app.applyChanges([], deleting: ["gone"], scope: "old")
        do {
            try await extensionStore.receiveVerified(item("gone"), scope: "old")
            XCTFail("deleted receipt accepted")
        } catch {}
        try await app.applyChanges([item("gone")], scope: "old")
        var saved = try await app.load(scope: "old")
        XCTAssertEqual(saved, [])
        try await app.erase(scope: "old")
        try await app.activate(scope: "new")
        do {
            try await extensionStore.receiveVerified(item("late"), scope: "old")
            XCTFail("old registration accepted")
        } catch {}
        saved = try await app.load(scope: "old")
        XCTAssertNil(saved)
    }
    func testMigrationPreservesNewerTargetPayloadAndRetriesAfterFailure() async throws {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let oldURL = folder.appendingPathComponent("legacy.sqlite")
        let newURL = folder.appendingPathComponent("shared.sqlite")
        let old = InboxStore(fileURL: oldURL)
        let target = InboxStore(fileURL: newURL, legacyURL: oldURL)
        try await old.save([item("same", read: true), item("other")], scope: "scope")
        var newer = item("same")
        newer.body = "newer verified content"
        try await target.save([newer], scope: "scope")
        var db: OpaquePointer?
        XCTAssertEqual(sqlite3_open(newURL.path, &db), SQLITE_OK)
        defer { sqlite3_close(db) }
        XCTAssertEqual(
            sqlite3_exec(
                db,
                "CREATE TRIGGER fail_import BEFORE INSERT ON inbox_messages WHEN NEW.id='other' BEGIN SELECT RAISE(ABORT,'injected'); END",
                nil, nil, nil), SQLITE_OK)
        do {
            try await target.prepareForApp()
            XCTFail("migration storage failure swallowed")
        } catch {}
        var saved = try await target.load(scope: "scope")
        XCTAssertEqual(saved?.count, 1)
        XCTAssertEqual(saved?.first?.body, newer.body)
        XCTAssertEqual(sqlite3_exec(db, "DROP TRIGGER fail_import", nil, nil, nil), SQLITE_OK)
        try await target.prepareForApp()
        saved = try await target.load(scope: "scope")
        XCTAssertEqual(saved?.count, 2)
        XCTAssertEqual(saved?.first(where: { $0.id == "same" })?.body, newer.body)
        XCTAssertFalse(try XCTUnwrap(saved?.first(where: { $0.id == "same" })).isUnread)
    }

    func testReceiptIdentityChangeRollsBackAndInactiveScopeCannotWrite() async throws {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
        try await store.activate(scope: "scope")
        let trace = LocalUnreadTrace()
        do {
            try await store.receiveVerified(item("late"), scope: "scope") {
                trace.add("check")
                return trace.values.count == 1
            }
            XCTFail("identity change accepted")
        } catch {}
        var saved = try await store.load(scope: "scope")
        XCTAssertEqual(saved, [])
        do {
            try await store.markLocalRead(["late"], scope: "other")
            XCTFail("inactive read accepted")
        } catch {}
        try await store.erase(scope: "scope")
        do {
            try await store.receiveVerified(item("late"), scope: "scope")
            XCTFail("erased scope reactivated")
        } catch {}
        saved = try await store.load(scope: "scope")
        XCTAssertNil(saved)
    }

    func testMalformedLegacyDataRemainsIntactAndCanRetryAfterRepair() async throws {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: folder) }
        let oldURL = folder.appendingPathComponent("legacy.sqlite")
        let original = Data("not a database".utf8)
        try original.write(to: oldURL)
        let store = InboxStore(fileURL: folder.appendingPathComponent("shared.sqlite"), legacyURL: oldURL)
        do {
            try await store.prepareForApp()
            XCTFail("corrupt history ignored")
        } catch {}
        XCTAssertEqual(try Data(contentsOf: oldURL), original)
        try FileManager.default.removeItem(at: oldURL)
        let old = InboxStore(fileURL: oldURL)
        try await old.save([item("recovered")], scope: "scope")
        try await store.prepareForApp()
        let saved = try await store.load(scope: "scope")
        XCTAssertEqual(saved?.map(\.id), ["recovered"])
    }

    func testOldSchemaReadStateIsBackfilledWithoutChangingThePayload() async throws {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: folder) }
        let file = folder.appendingPathComponent("old.sqlite")
        var db: OpaquePointer?
        XCTAssertEqual(sqlite3_open(file.path, &db), SQLITE_OK)
        defer { sqlite3_close(db) }
        XCTAssertEqual(
            sqlite3_exec(
                db,
                "CREATE TABLE inbox_scopes(scope TEXT PRIMARY KEY); INSERT INTO inbox_scopes VALUES('scope'); CREATE TABLE inbox_messages(scope TEXT,id TEXT,created_at REAL,payload BLOB,PRIMARY KEY(scope,id));",
                nil, nil, nil), SQLITE_OK)
        let original = item("old", read: true)
        let data = try JSONEncoder().encode(original)
        var statement: OpaquePointer?
        XCTAssertEqual(
            sqlite3_prepare_v2(db, "INSERT INTO inbox_messages VALUES('scope','old',0,?)", -1, &statement, nil),
            SQLITE_OK)
        defer { sqlite3_finalize(statement) }
        _ = data.withUnsafeBytes {
            sqlite3_bind_blob(
                statement, 1, $0.baseAddress, Int32($0.count), unsafeBitCast(-1, to: sqlite3_destructor_type.self))
        }
        XCTAssertEqual(sqlite3_step(statement), SQLITE_DONE)
        let store = InboxStore(fileURL: file)
        let saved = try await store.load(scope: "scope")
        XCTAssertEqual(saved, [original])
        let count = try await store.unreadCount(scope: "scope")
        XCTAssertEqual(count, 0)
    }

    func testReadCannotInsertPendingMessagesOutsideItsSelection() async throws {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
        try await store.activate(scope: "scope")
        do {
            try await store.markLocalRead(["selected"], scope: "scope", pendingItems: [item("unselected")])
            XCTFail("unselected pending row inserted")
        } catch {}
        let saved = try await store.load(scope: "scope")
        XCTAssertEqual(saved, [])
    }

    func testReadTransactionFailureAndGeneratedReplayAreAtomic() async throws {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let file = folder.appendingPathComponent("shared.sqlite")
        let store = InboxStore(fileURL: file)
        try await store.activate(scope: "scope")
        for i in 0..<80 { try await store.receiveVerified(item("n\(i)"), scope: "scope") }
        var db: OpaquePointer?
        XCTAssertEqual(sqlite3_open(file.path, &db), SQLITE_OK)
        defer { sqlite3_close(db) }
        XCTAssertEqual(
            sqlite3_exec(
                db,
                "CREATE TRIGGER fail_read BEFORE UPDATE OF read_at ON inbox_messages BEGIN SELECT RAISE(ABORT,'injected'); END",
                nil, nil, nil), SQLITE_OK)
        do {
            try await store.markLocalRead(["n0", "n1"], scope: "scope")
            XCTFail("read storage error swallowed")
        } catch {}
        var count = try await store.unreadCount(scope: "scope")
        XCTAssertEqual(count, 80)
        XCTAssertEqual(sqlite3_exec(db, "DROP TRIGGER fail_read", nil, nil, nil), SQLITE_OK)
        var read = Set<String>()
        for round in 0..<10 {
            let ids = Set((0..<80).filter { ($0 + round) % 7 == 0 }.map { "n\($0)" })
            read.formUnion(ids)
            try await store.markLocalRead(ids, scope: "scope")
            for id in ids { try await store.receiveVerified(item(id), scope: "scope") }
            count = try await store.unreadCount(scope: "scope")
            XCTAssertEqual(count, 80 - read.count)
        }
    }
}

private final class UnreadOfflineProtocol: URLProtocol, @unchecked Sendable {
    static let lock = NSLock()
    nonisolated(unsafe) static var calls = 0
    nonisolated(unsafe) static var registration: Data?
    override class func canInit(with request: URLRequest) -> Bool { request.url?.host == "local-unread.invalid" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        Self.lock.withLock { Self.calls += 1 }
        if request.url?.path == "/v1/devices", let data = Self.lock.withLock({ Self.registration }) {
            client?.urlProtocol(
                self,
                didReceive: HTTPURLResponse(url: request.url!, statusCode: 201, httpVersion: nil, headerFields: nil)!,
                cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
            return
        }
        client?.urlProtocol(self, didFailWithError: URLError(.notConnectedToInternet))
    }
    override func stopLoading() {}
}

@MainActor
final class LocalUnreadModelTests: XCTestCase {
    func testRegistrationActivatesLocalInboxBeforeReturning() async throws {
        let oldServer = MeowShared.server
        let oldToken = MeowShared.deviceToken
        let oldDevice = MeowShared.deviceID
        let oldKey = Keychain.get("device_key")
        let oldKeyID = MeowShared.defaults.string(forKey: "device_key_id")
        let server = URL(string: "https://local-unread.invalid")!
        MeowShared.server = server
        MeowShared.deviceToken = nil
        MeowShared.deviceID = nil
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer {
            URLProtocol.unregisterClass(UnreadOfflineProtocol.self)
            UnreadOfflineProtocol.lock.withLock { UnreadOfflineProtocol.registration = nil }
            MeowShared.server = oldServer
            MeowShared.deviceToken = oldToken
            MeowShared.deviceID = oldDevice
            Keychain.set(oldKey, for: "device_key")
            MeowShared.defaults.set(oldKeyID, forKey: "device_key_id")
            try? FileManager.default.removeItem(at: folder)
        }
        UnreadOfflineProtocol.lock.withLock {
            UnreadOfflineProtocol.registration = Data(
                #"{"device":{"id":"dev_registration","name":"fixture","apns_env":"production","has_apns_token":false,"created_at":"2026-10-29T00:00:00Z","last_seen_at":"2026-10-29T00:00:00Z"},"device_token":"mdt_disposable_registered","key":{"id":"key_fixture","name":"Default","hint":"fixture","scopes":["notify"],"created_at":"2026-10-29T00:00:00Z","secret":"meow_disposable_registration"}}"#
                    .utf8)
        }
        URLProtocol.registerClass(UnreadOfflineProtocol.self)
        let store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
        let model = AppModel(inboxStore: store)
        await model.register()
        XCTAssertTrue(model.isRegistered)
        let item = MeowNotification(
            id: "ntf_first_push", type: .info, title: "first push", body: "body", source: "fixture", priority: "normal",
            pushStatus: "accepted", createdAt: .now)
        let scope = InboxStore.scope(server: server, token: "mdt_disposable_registered")
        try await store.receiveVerified(item, scope: scope)
        let count = try await store.unreadCount(scope: scope)
        XCTAssertEqual(count, 1)
    }

    func testResetDuringMigrationCannotReactivateOldRegistration() async throws {
        let server = URL(string: "https://local-unread.invalid")!
        let token = "mdt_disposable_reset"
        let previousServer = MeowShared.server
        let previousToken = MeowShared.deviceToken
        MeowShared.server = server
        MeowShared.deviceToken = token
        URLProtocol.registerClass(UnreadOfflineProtocol.self)
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer {
            URLProtocol.unregisterClass(UnreadOfflineProtocol.self)
            MeowShared.server = previousServer
            MeowShared.deviceToken = previousToken
            try? FileManager.default.removeItem(at: folder)
        }
        let oldURL = folder.appendingPathComponent("legacy.sqlite")
        let file = folder.appendingPathComponent("shared.sqlite")
        let old = InboxStore(fileURL: oldURL)
        let scope = InboxStore.scope(server: server, token: token)
        let item = MeowNotification(
            id: "ntf_old", type: .info, title: "old", body: "body", source: "fixture", priority: "normal",
            pushStatus: "accepted", createdAt: .now)
        try await old.save([item], scope: scope)
        let store = InboxStore(fileURL: file, legacyURL: oldURL)
        _ = try await store.load(scope: scope)
        var db: OpaquePointer?
        XCTAssertEqual(sqlite3_open(file.path, &db), SQLITE_OK)
        defer { sqlite3_close(db) }
        XCTAssertEqual(sqlite3_exec(db, "BEGIN IMMEDIATE", nil, nil, nil), SQLITE_OK)
        let queue = ReplyQueue(fileURL: folder.appendingPathComponent("queue.json")) { _ in
            throw URLError(.unsupportedURL)
        }
        let model = AppModel(replies: queue, inboxStore: store)
        let refresh = Task { await model.refresh() }
        while !model.isLoading { await Task.yield() }
        let reset = Task { await model.reset(server: URL(string: "https://new.invalid")!) }
        while !model.isChangingServer { await Task.yield() }
        XCTAssertEqual(sqlite3_exec(db, "COMMIT", nil, nil, nil), SQLITE_OK)
        let didReset = await reset.value
        XCTAssertTrue(didReset)
        await refresh.value
        do {
            try await store.receiveVerified(item, scope: scope)
            XCTFail("old scope reactivated")
        } catch {}
        let saved = try await store.load(scope: scope)
        XCTAssertNil(saved)
        XCTAssertTrue(model.savedNotifications.isEmpty)
    }

    func testOfflineReadAndCountsNeverNeedHTTPAndSurviveRestart() async throws {
        let server = URL(string: "https://local-unread.invalid")!
        let token = "mdt_disposable_unread"
        let oldServer = MeowShared.server
        let oldToken = MeowShared.deviceToken
        MeowShared.server = server
        MeowShared.deviceToken = token
        URLProtocol.registerClass(UnreadOfflineProtocol.self)
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer {
            URLProtocol.unregisterClass(UnreadOfflineProtocol.self)
            MeowShared.server = oldServer
            MeowShared.deviceToken = oldToken
            try? FileManager.default.removeItem(at: folder)
        }
        let store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
        let scope = InboxStore.scope(server: server, token: token)
        let items = (0..<1000).map {
            MeowNotification(
                id: "ntf_local_\($0)", type: .info, title: "fixture", body: "本地历史", source: "fixture",
                priority: "normal", pushStatus: "accepted", createdAt: .now)
        }
        try await store.save(items, scope: scope)
        let model = AppModel(inboxStore: store)
        await model.refresh()
        UnreadOfflineProtocol.lock.withLock { UnreadOfflineProtocol.calls = 0 }
        await model.markMatchingRead(Set(items.prefix(500).map(\.id)))
        XCTAssertEqual(model.counts.unread, 500)
        XCTAssertTrue(model.failedGroupReadIDs.isEmpty)
        await model.refreshCounts()
        XCTAssertEqual(UnreadOfflineProtocol.lock.withLock { UnreadOfflineProtocol.calls }, 0)
        let saved = try await store.load(scope: scope)
        XCTAssertEqual(saved?.filter(\.isUnread).count, 500)
        await model.markAllRead()
        XCTAssertEqual(model.counts.unread, 0)
        XCTAssertEqual(UnreadOfflineProtocol.lock.withLock { UnreadOfflineProtocol.calls }, 0)
    }
}

final class LocalUnreadDeliveryTests: XCTestCase {
    func testBadgeIsReadAfterReceiptWorkRatherThanReusingAnOlderCount() async throws {
        let identity = NotificationIdentity(
            server: URL(string: "https://badge.invalid")!, token: "mdt_fixture", deviceID: "dev_fixture",
            bindingID: "bind_fixture")
        let original = UNMutableNotificationContent()
        original.userInfo = ["meow": ["id": "ntf_race", "relay": true, "binding_id": "bind_fixture"]]
        let shown = expectation(description: "fresh badge")
        let reporting = expectation(description: "report pending")
        let gate = LocalUnreadGate()
        let state = LocalUnreadBadgeValue()
        let item = MeowNotification(
            id: "ntf_race", type: .info, title: "Full", body: "body", source: "fixture", priority: "normal",
            pushStatus: "accepted", createdAt: .now)
        let delivery = NotificationDelivery(
            original: original, currentIdentity: { identity }, fetch: { _, _ in item },
            report: { _, _ in
                reporting.fulfill()
                await gate.wait()
            }, record: { _, _ in }, counts: { _ in state.read() },
            handler: { content in
                XCTAssertEqual(content.badge, 0)
                shown.fulfill()
            })
        delivery.start()
        await fulfillment(of: [reporting], timeout: 1)
        try await Task.sleep(for: .milliseconds(30))
        state.markRead()
        await gate.release()
        await fulfillment(of: [shown], timeout: 1)
    }

    func testOldServerBadgeCannotOverrideLocalStateOnFailedFetch() async throws {
        let identity = NotificationIdentity(
            server: URL(string: "https://badge.invalid")!, token: "mdt_fixture", deviceID: "dev_fixture")
        let original = UNMutableNotificationContent()
        original.title = "Safe direct preview"
        original.body = "body"
        original.badge = 999
        original.userInfo = ["meow": ["id": "ntf_direct", "device_id": "dev_fixture"]]
        let shown = expectation(description: "safe fallback")
        let delivery = NotificationDelivery(
            original: original, currentIdentity: { identity },
            fetch: { _, _ in throw URLError(.notConnectedToInternet) },
            report: { _, _ in XCTFail("unverified message reported") },
            handler: { content in
                XCTAssertEqual(content.title, "Safe direct preview")
                XCTAssertNil(content.badge)
                shown.fulfill()
            })
        delivery.start()
        await fulfillment(of: [shown], timeout: 1)
    }

    func testSlowLocalBadgeReadCannotApplyALateCount() async throws {
        let identity = NotificationIdentity(
            server: URL(string: "https://badge.invalid")!, token: "mdt_fixture", deviceID: "dev_fixture",
            bindingID: "bind_fixture")
        let original = UNMutableNotificationContent()
        original.userInfo = ["meow": ["id": "ntf_late_badge", "relay": true, "binding_id": "bind_fixture"]]
        let gate = LocalUnreadGate()
        let countStarted = expectation(description: "local count started")
        let shown = expectation(description: "count deadline")
        let item = MeowNotification(
            id: "ntf_late_badge", type: .info, title: "Full", body: "body", source: "fixture", priority: "normal",
            pushStatus: "accepted", createdAt: .now)
        let delivery = NotificationDelivery(
            original: original, currentIdentity: { identity }, fetch: { _, _ in item }, report: { _, _ in },
            counts: { _ in
                countStarted.fulfill()
                await gate.wait()
                return 999
            },
            handler: { content in
                XCTAssertNil(content.badge)
                shown.fulfill()
            })
        delivery.start()
        await fulfillment(of: [countStarted, shown], timeout: 1)
        await gate.release()
        try await Task.sleep(for: .milliseconds(30))
        delivery.expire()
    }

    func testHydratedDirectExpiryIsCheckedAgainAtDisplay() throws {
        let identity = NotificationIdentity(
            server: URL(string: "https://unread.invalid")!, token: "mdt_fixture", deviceID: "dev_fixture")
        let original = UNMutableNotificationContent()
        original.userInfo = ["meow": ["id": "ntf_direct_expiry", "device_id": "dev_fixture"]]
        let now = Date()
        let item = MeowNotification(
            id: "ntf_direct_expiry", type: .question, title: "Question", body: "body", source: "fixture",
            priority: "normal", pushStatus: "accepted", createdAt: now,
            question: .init(
                id: "q_direct", status: .pending, choices: ["approve", "reject"], expiresAt: now.addingTimeInterval(1)))
        let hydrated = try XCTUnwrap(
            NotificationContent.hydrate(item, original: original, identity: identity, now: now))
        let shown = NotificationContent.forDisplay(hydrated, identity: identity, now: now.addingTimeInterval(2))
        XCTAssertEqual(shown.categoryIdentifier, NotificationCategory.info)
    }

    func testBothPushPathsPersistVerifiedContentBeforeReadingLocalBadge() async throws {
        let identity = NotificationIdentity(
            server: URL(string: "https://unread.invalid")!, token: "mdt_fixture", deviceID: "dev_fixture",
            bindingID: "bind_fixture")
        for relay in [true, false] {
            let shown = expectation(description: "local badge \(relay)")
            let calls = LocalUnreadTrace()
            let original = UNMutableNotificationContent()
            original.title = "Direct preview"
            original.body = "Preview"
            original.badge = 999
            let metadata: [String: Any] =
                relay
                ? ["id": "ntf_local", "relay": true, "binding_id": "bind_fixture"]
                : ["id": "ntf_local", "device_id": "dev_fixture"]
            original.userInfo = ["meow": metadata]
            let item = MeowNotification(
                id: "ntf_local", type: .info, title: "Full", body: "完整正文", source: "fixture", priority: "normal",
                pushStatus: "accepted", createdAt: .now)
            let delivery = NotificationDelivery(
                original: original, currentIdentity: { identity },
                fetch: { _, _ in
                    calls.add("fetch")
                    return item
                }, report: { _, _ in }, record: { _, _ in calls.add("save") },
                counts: { _ in
                    calls.add("count")
                    return 3
                },
                handler: { content in
                    XCTAssertEqual(content.badge, 3)
                    shown.fulfill()
                })
            delivery.start()
            await fulfillment(of: [shown], timeout: 2)
            XCTAssertEqual(calls.values, ["fetch", "save", "count"])
        }
    }
}
private final class LocalUnreadTrace: @unchecked Sendable {
    private let lock = NSLock()
    private var events: [String] = []
    func add(_ event: String) { lock.withLock { events.append(event) } }
    var values: [String] { lock.withLock { events } }
}

private final class LocalUnreadBadgeValue: @unchecked Sendable {
    private let lock = NSLock()
    private var count = 1
    func read() -> Int { lock.withLock { count } }
    func markRead() { lock.withLock { count = 0 } }
}
private actor LocalUnreadGate {
    private var opened = false
    private var waiter: CheckedContinuation<Void, Never>?
    func wait() async {
        if opened { return }
        await withCheckedContinuation { waiter = $0 }
    }
    func release() {
        opened = true
        waiter?.resume()
        waiter = nil
    }
}
