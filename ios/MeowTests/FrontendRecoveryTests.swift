import XCTest
@testable import Meow

private final class RecoveryURLProtocol: URLProtocol, @unchecked Sendable {
    static let lock = NSLock()
    static var handler: ((URLRequest) throws -> (Int, Data))?
    static func install(_ handler: @escaping (URLRequest) throws -> (Int, Data)) {
        lock.withLock { Self.handler = handler }
        URLProtocol.registerClass(Self.self)
    }
    static func uninstall() { URLProtocol.unregisterClass(Self.self); lock.withLock { handler = nil } }
    override class func canInit(with request: URLRequest) -> Bool { request.url?.host?.hasSuffix(".invalid") == true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        DispatchQueue.global().async { self.respond() }
    }
    private func respond() {
        do {
            guard let handler = Self.lock.withLock({ Self.handler }) else { throw URLError(.cancelled) }
            let (code, data) = try handler(request)
            client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: code,
                httpVersion: nil, headerFields: nil)!, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        } catch { client?.urlProtocol(self, didFailWithError: error) }
    }
    override func stopLoading() {}
}

@MainActor
final class FrontendRecoveryTests: XCTestCase {
    private let server = URL(string: "https://recovery.invalid")!
    private var file: URL!
    private var oldServer: URL?
    private var oldToken: String?
    private var oldDevice: String?
    private var oldKey: String?
    private var oldKeyID: String?
    private var oldBinding: GatewayBinding?
    private var oldOnboarded = false

    override func setUp() async throws {
        oldServer = MeowShared.server; oldToken = MeowShared.deviceToken; oldDevice = MeowShared.deviceID
        oldOnboarded = MeowShared.defaults.bool(forKey: "onboarded")
        oldKey = Keychain.get("device_key"); oldKeyID = MeowShared.defaults.string(forKey: "device_key_id")
        oldBinding = MeowShared.gatewayBinding; MeowShared.saveGatewayBinding(nil)
        MeowShared.server = server; MeowShared.deviceToken = "mdt_recovery"; MeowShared.deviceID = "dev_recovery"
        // Each test owns a fresh fake-device history; refresh no longer erases it.
        try await InboxStore.shared.erase(scope: InboxStore.scope(server: server, token: "mdt_recovery"))
        file = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString + ".json")
    }
    override func tearDown() async throws {
        RecoveryURLProtocol.uninstall()
        MeowShared.server = oldServer; MeowShared.deviceToken = oldToken; MeowShared.deviceID = oldDevice
        MeowShared.defaults.set(oldOnboarded, forKey: "onboarded")
        Keychain.set(oldKey, for: "device_key"); MeowShared.defaults.set(oldKeyID, forKey: "device_key_id")
        MeowShared.saveGatewayBinding(oldBinding)
        try? FileManager.default.removeItem(at: file)
    }
    private var notificationJSON: Data {
        Data(#"{"id":"ntf_recovery","type":"info","title":"Test","body":"Test","source":"Agent","priority":"normal","push_status":"accepted","created_at":"2026-10-17T00:00:00Z","read_at":null}"#.utf8)
    }

    func testRetryRefusalReportsFailureInsteadOfSilentlyDisappearing() async {
        let offline = ReplyQueue(fileURL: file) { _ in throw URLError(.notConnectedToInternet) }
        _ = await offline.submit(questionID: "qst_recovery", answer: "original answer")
        let recovered = ReplyQueue(fileURL: file) { _ in
            throw MeowAPIError(status: 404, code: "not_found", message: "Question no longer exists")
        }
        let model = AppModel(replies: recovered)
        await model.flushReplies()
        XCTAssertTrue(model.queuedReplies.isEmpty)
        XCTAssertTrue(model.alert != nil || model.toast != nil, "the user must learn that the saved answer was refused")
    }

    func testFailedReadRollsBackAndExplainsFailure() async throws {
        let attempted = expectation(description: "read attempted")
        let json = notificationJSON
        RecoveryURLProtocol.install { request in
            if request.url!.path.hasSuffix("/read") {
                attempted.fulfill()
                throw URLError(.notConnectedToInternet)
            }
            return (200, json)
        }
        let model = AppModel()
        let loaded = await model.loadDetail("ntf_recovery")
        XCTAssertTrue(loaded)
        await model.markRead("ntf_recovery")
        await fulfillment(of: [attempted], timeout: 3)
        try await Task.sleep(for: .milliseconds(100))
        XCTAssertTrue(try XCTUnwrap(model.notification(id: "ntf_recovery")).isUnread)
        XCTAssertNotNil(model.toast)
    }

    func testLegacyQueueRefusalsSurviveRestartWithoutResending() async throws {
        for status in [401, 404, 409, 410] {
            let original = PendingReply(idempotencyKey: "ios-original-\(status)", questionID: "qst_recovery",
                                        answer: "original answer", createdAt: .now)
            try JSONEncoder().encode([original]).write(to: file)
            let queue = ReplyQueue(fileURL: file) { _ in
                throw MeowAPIError(status: status, code: status == 404 ? "not_found" : "refused", message: "Refused \(status)")
            }
            let model = AppModel(replies: queue)
            await model.flushReplies()
            let failed = try XCTUnwrap(model.failedReplies.first)
            XCTAssertEqual(failed.idempotencyKey, original.idempotencyKey)
            XCTAssertEqual(failed.answer, original.answer)
            XCTAssertEqual(failed.failure?.status, status)
            let failure = try XCTUnwrap(failed.failure)
            XCTAssertEqual(failure.error.status, status)
            XCTAssertEqual(failure.error.message, "Refused \(status)")
            let outstanding = await queue.outstandingCount
            XCTAssertEqual(outstanding, 0, "terminal failures must not block switching as pending replies")
            XCTAssertTrue(model.queuedReplies.isEmpty)
            XCTAssertNotNil(model.alert)
            let restarted = ReplyQueue(fileURL: file) { _ in XCTFail("terminal failures must not resend"); throw URLError(.badURL) }
            let reloaded = AppModel(replies: restarted)
            await reloaded.flushReplies()
            XCTAssertEqual(reloaded.failedReplies, [failed])
            let pending = await restarted.pending
            XCTAssertTrue(pending.isEmpty)
            await reloaded.dismissFailedReply(failed)
            let disk = try JSONDecoder().decode([PendingReply].self, from: Data(contentsOf: file))
            XCTAssertTrue(disk.isEmpty)
        }
    }

    func testFailureArchiveWriteErrorPreservesOriginalDurableReply() async throws {
        let directory = file!
        let backup = directory.appendingPathExtension("backup")
        defer { try? FileManager.default.removeItem(at: backup) }
        let queueFile = directory.appending(path: "queue.json")
        let offline = ReplyQueue(fileURL: queueFile) { _ in throw URLError(.notConnectedToInternet) }
        _ = await offline.submit(questionID: "qst_recovery", answer: "preserve me")
        let queue = ReplyQueue(fileURL: queueFile) { _ in throw MeowAPIError(status: 404, code: "not_found", message: "Gone") }
        try FileManager.default.moveItem(at: directory, to: backup)
        try Data().write(to: directory)
        let results = await queue.flush()
        guard case .notSaved = results.first?.1 else { return XCTFail("archiving failure must be visible") }
        let pending = await queue.pending
        XCTAssertEqual(pending.first?.answer, "preserve me")
        let canSwitch = await queue.pauseIfIdle()
        XCTAssertFalse(canSwitch)
        let durable = try JSONDecoder().decode([PendingReply].self, from: Data(contentsOf: backup.appending(path: "queue.json")))
        XCTAssertEqual(pending, durable)
        try FileManager.default.removeItem(at: directory)
        try FileManager.default.moveItem(at: backup, to: directory)
        _ = await queue.flush()
        let failed = await queue.failed
        XCTAssertEqual(failed.first?.answer, "preserve me")
        let finalPending = await queue.pending
        XCTAssertTrue(finalPending.isEmpty)
    }

    func testLateRefusalCannotReplaceConcurrentAcceptedReply() async throws {
        let firstStarted = expectation(description: "first request")
        let secondStarted = expectation(description: "duplicate request")
        let held = ReplyQueueTests.HeldServer(started: [firstStarted, secondStarted])
        let queue = ReplyQueue(fileURL: file) { try await held.send($0) }
        let first = Task { await queue.submit(questionID: "qst_recovery", answer: "approve") }
        await fulfillment(of: [firstStarted], timeout: 3)
        let second = Task { await queue.submit(questionID: "qst_recovery", answer: "approve") }
        await fulfillment(of: [secondStarted], timeout: 3)
        let outstanding = await queue.outstandingCount
        XCTAssertEqual(outstanding, 1, "duplicate requests belong to one question")
        await held.finish(0, with: accepted())
        _ = await first.value
        await held.fail(1, with: MeowAPIError(status: 404, code: "not_found", message: "Gone"))
        let result = await second.value
        guard case .discarded = result else { return XCTFail("a stale refusal must not claim the accepted answer failed") }
        let failed = await queue.failed
        let pending = await queue.pending
        XCTAssertTrue(failed.isEmpty)
        XCTAssertTrue(pending.isEmpty)
    }

    func testSameServerResetAndRegistrationRecoversInvalidCredentials() async throws {
        let queue = ReplyQueue(fileURL: file) { _ in throw URLError(.badURL) }
        let model = AppModel(replies: queue)
        let oldURL = model.server
        let registration = Data(#"{"device":{"id":"dev_new","name":"Test","apns_env":"production","has_apns_token":false,"created_at":"2026-10-17T00:00:00Z","last_seen_at":"2026-10-17T00:00:00Z"},"device_token":"mdt_new","key":{"id":"key_new","name":"Default","hint":"meow_…","scopes":["notify","ask"],"created_at":"2026-10-17T00:00:00Z","secret":"meow_new"}}"#.utf8)
        RecoveryURLProtocol.install { request in
            if request.url!.path == "/v1/devices" {
                XCTAssertEqual(request.httpMethod, "POST")
                XCTAssertNil(request.value(forHTTPHeaderField: "Authorization"))
                return (201, registration)
            }
            return (401, Data(#"{"error":{"code":"unauthorized","message":"Revoked"}}"#.utf8))
        }
        await model.refresh()
        XCTAssertNotNil(model.loadError)
        let reset = await model.reset(server: try XCTUnwrap(oldURL))
        XCTAssertTrue(reset)
        XCTAssertEqual(model.server, oldURL)
        XCTAssertNil(MeowShared.deviceToken)
        XCTAssertFalse(model.hasFinishedOnboarding)
        await model.register()
        XCTAssertTrue(model.isRegistered)
        XCTAssertEqual(MeowShared.deviceToken, "mdt_new")
        XCTAssertEqual(model.deviceKey, "meow_new")
        XCTAssertEqual(MeowShared.deviceID, "dev_new")
        XCTAssertEqual(model.server, oldURL)
    }

    func testSameServerResetRequiresExplicitDiscardForPendingReply() async {
        let queue = ReplyQueue(fileURL: file) { _ in throw URLError(.notConnectedToInternet) }
        _ = await queue.submit(questionID: "qst_recovery", answer: "keep until confirmed")
        let model = AppModel(replies: queue)
        let cancelled = await model.reset(server: server)
        XCTAssertFalse(cancelled)
        XCTAssertTrue(model.isRegistered)
        XCTAssertEqual(MeowShared.deviceToken, "mdt_recovery")
        let pending = await queue.pending
        XCTAssertEqual(pending.first?.answer, "keep until confirmed")
        let confirmed = await model.reset(server: server, discardPendingReplies: true)
        XCTAssertTrue(confirmed)
        XCTAssertFalse(model.isRegistered)
        let remaining = await queue.pending
        XCTAssertTrue(remaining.isEmpty)
    }

    func testFailedRecordsAreClearedByServerReset() async {
        let queue = ReplyQueue(fileURL: file) { _ in throw MeowAPIError(status: 410, code: "expired", message: "Expired") }
        _ = await queue.submit(questionID: "qst_recovery", answer: "old failure")
        let model = AppModel(replies: queue)
        await model.flushReplies()
        XCTAssertEqual(model.failedReplies.count, 1)
        let switched = await model.reset(server: URL(string: "https://other.invalid")!)
        XCTAssertTrue(switched)
        XCTAssertTrue(model.failedReplies.isEmpty)
        let failed = await queue.failed
        XCTAssertTrue(failed.isEmpty)
    }

    private func accepted() -> MeowQuestion {
        MeowQuestion(id: "qst_recovery", status: .answered, question: "Question", choices: nil, answer: "approve",
                     createdAt: .now, expiresAt: .now.addingTimeInterval(60), resolvedAt: .now, notificationId: "ntf_recovery")
    }

    private func installInbox(read: @escaping (URLRequest) throws -> (Int, Data), unread: Int = 2) {
        let json = notificationJSON
        RecoveryURLProtocol.install { request in
            if request.url!.path == "/v1/notifications/ntf_recovery/read" { return try read(request) }
            if request.url!.path == "/v1/inbox/counts" { return (200, Data("{\"unread\":\(unread),\"pending\":0}".utf8)) }
            if request.url!.path == "/v1/inbox/read" { return (204, Data()) }
            if request.url!.path == "/v1/inbox" { return (200, Data("{\"notifications\":[\(String(decoding: json, as: UTF8.self))],\"next_cursor\":null}".utf8)) }
            return (200, json)
        }
    }

    func testReadRollbackRestoresCountAndCoalescesDuplicateTaps() async {
        let started = expectation(description: "read requested once")
        started.assertForOverFulfill = true
        let gate = DispatchSemaphore(value: 0)
        installInbox { _ in
            started.fulfill(); _ = gate.wait(timeout: .now() + 5)
            return (503, Data())
        }
        let model = AppModel()
        await model.refresh()
        XCTAssertEqual(model.counts.unread, 2)
        let first = Task { await model.markRead("ntf_recovery") }
        await fulfillment(of: [started], timeout: 3)
        XCTAssertEqual(model.counts.unread, 1)
        await model.markRead("ntf_recovery")
        gate.signal(); await first.value
        XCTAssertEqual(model.counts.unread, 2)
        XCTAssertEqual(model.notification(id: "ntf_recovery")?.isUnread, true)
        XCTAssertNotNil(model.toast)
        installInbox { _ in (204, Data()) }
        model.toast = nil
        await model.markRead("ntf_recovery")
        XCTAssertEqual(model.counts.unread, 1)
        XCTAssertEqual(model.notification(id: "ntf_recovery")?.isUnread, false)
        XCTAssertNil(model.toast)
    }

    func testReadFailureCannotUndoMarkAllRead() async {
        let started = expectation(description: "read requested")
        let gate = DispatchSemaphore(value: 0)
        installInbox { _ in started.fulfill(); _ = gate.wait(timeout: .now() + 5); throw URLError(.timedOut) }
        let model = AppModel(); await model.refresh()
        let reading = Task { await model.markRead("ntf_recovery") }
        await fulfillment(of: [started], timeout: 3)
        await model.markAllRead()
        gate.signal(); await reading.value
        XCTAssertEqual(model.counts.unread, 0)
        XCTAssertEqual(model.notification(id: "ntf_recovery")?.isUnread, false)
        XCTAssertNil(model.toast)
    }

    func testReadFailureCannotChangeNewSession() async {
        let started = expectation(description: "read requested")
        let gate = DispatchSemaphore(value: 0)
        installInbox { _ in started.fulfill(); _ = gate.wait(timeout: .now() + 5); throw URLError(.timedOut) }
        let queue = ReplyQueue(fileURL: file) { _ in throw URLError(.badURL) }
        let model = AppModel(replies: queue); await model.refresh()
        let reading = Task { await model.markRead("ntf_recovery") }
        await fulfillment(of: [started], timeout: 3)
        let reset = await model.reset(server: URL(string: "https://other.invalid")!)
        XCTAssertTrue(reset)
        gate.signal(); await reading.value
        XCTAssertTrue(model.notifications.isEmpty)
        XCTAssertEqual(model.counts.unread, 0)
        XCTAssertNil(model.toast)
    }

    func testReadFailureReconcilesNewerServerCountWithOptimisticRead() async {
        let started = expectation(description: "read requested")
        let gate = DispatchSemaphore(value: 0)
        installInbox { _ in started.fulfill(); _ = gate.wait(timeout: .now() + 5); throw URLError(.timedOut) }
        let model = AppModel(); await model.refresh()
        let reading = Task { await model.markRead("ntf_recovery") }
        await fulfillment(of: [started], timeout: 3)
        await model.refreshCounts()
        XCTAssertEqual(model.counts.unread, 1, "the pending local read survives the refreshed server snapshot")
        gate.signal(); await reading.value
        XCTAssertEqual(model.counts.unread, 2, "the server snapshot already includes the unread notification")
        XCTAssertEqual(model.notification(id: "ntf_recovery")?.isUnread, true)
    }

    func testReadFailureCannotUndoAcceptedAnswer() async {
        let started = expectation(description: "read requested")
        let gate = DispatchSemaphore(value: 0)
        let n = MeowNotification(id: "ntf_recovery", type: .question, title: "Question", body: "Body", source: "Agent",
            priority: "normal", pushStatus: "accepted", createdAt: .now,
            question: .init(id: "qst_recovery", status: .pending, choices: nil, expiresAt: .now.addingTimeInterval(60)))
        let encoder = JSONEncoder(); encoder.dateEncodingStrategy = .iso8601
        let data = try! encoder.encode(n)
        RecoveryURLProtocol.install { request in
            if request.url!.path.hasSuffix("/read") { started.fulfill(); _ = gate.wait(timeout: .now() + 5); throw URLError(.timedOut) }
            if request.url!.path.hasSuffix("/counts") { return (200, Data(#"{"unread":0,"pending":0}"#.utf8)) }
            if request.url!.path == "/v1/inbox" {
                return (200, Data("{\"notifications\":[\(String(decoding: data, as: UTF8.self))],\"next_cursor\":null}".utf8))
            }
            return (200, data)
        }
        let result = accepted()
        let queue = ReplyQueue(fileURL: file) { _ in result }
        let model = AppModel(replies: queue)
        _ = await model.loadDetail("ntf_recovery")
        await model.refresh()
        XCTAssertEqual(model.notifications.first?.question?.status, .pending)
        let reading = Task { await model.markRead("ntf_recovery") }
        await fulfillment(of: [started], timeout: 3)
        let answered = await model.answer("qst_recovery", with: "approve")
        XCTAssertEqual(answered, .sent)
        gate.signal(); await reading.value
        XCTAssertEqual(model.notification(id: "ntf_recovery")?.isUnread, false)
        XCTAssertEqual(model.notification(id: "ntf_recovery")?.question?.status, .answered)
        XCTAssertNil(model.toast)
    }

    func testAcceptedNewSubmissionClearsOlderFailureForSameQuestion() async {
        let original = ReplyQueue(fileURL: file) { _ in throw MeowAPIError(status: 404, code: "not_found", message: "Gone") }
        _ = await original.submit(questionID: "qst_recovery", answer: "old answer")
        let result = accepted()
        let queue = ReplyQueue(fileURL: file) { _ in result }
        let model = AppModel(replies: queue)
        await model.flushReplies()
        XCTAssertEqual(model.failedReplies.count, 1)
        let outcome = await model.answer("qst_recovery", with: "new answer")
        XCTAssertEqual(outcome, .sent)
        XCTAssertTrue(model.failedReplies.isEmpty)
        let failed = await queue.failed
        XCTAssertTrue(failed.isEmpty)
    }

    func testFailedRecordDeleteAndResetWriteErrorsKeepRecord() async throws {
        let directory = file!
        let backup = directory.appendingPathExtension("backup")
        defer { try? FileManager.default.removeItem(at: backup) }
        let queue = ReplyQueue(fileURL: directory.appending(path: "queue.json")) { _ in
            throw MeowAPIError(status: 410, code: "expired", message: "Expired")
        }
        _ = await queue.submit(questionID: "qst_recovery", answer: "keep failed answer")
        let model = AppModel(replies: queue)
        await model.flushReplies()
        let failed = try XCTUnwrap(model.failedReplies.first)
        try FileManager.default.moveItem(at: directory, to: backup)
        try Data().write(to: directory)
        await model.dismissFailedReply(failed)
        XCTAssertNotNil(model.alert)
        XCTAssertEqual(model.failedReplies, [failed])
        let reset = await model.reset(server: server)
        XCTAssertFalse(reset)
        XCTAssertTrue(model.isRegistered)
        XCTAssertEqual(model.alert,
                       String(localized: "Couldn't prepare saved reply records. Your server and records have been kept. Check available storage and try again."),
                       "a failed record write must explain storage, not claim an answer is waiting")
        let stored = await queue.failed
        XCTAssertEqual(stored, [failed])
        let durable = try JSONDecoder().decode([PendingReply].self, from: Data(contentsOf: backup.appending(path: "queue.json")))
        XCTAssertEqual(durable, [failed])
    }

    func testFailureSerializationPreservesGeneratedAnswersAndKeys() async throws {
        // Deterministic generated corpus exercises Unicode, control characters,
        // long answers and varying keys without adding a property-test package.
        var seed: UInt64 = 73
        let alphabet = Array("abc猫😺é\n\t\"\\")
        for sample in 0..<64 {
            var answer = ""
            for _ in 0..<(sample * 7) {
                seed = seed &* 6364136223846793005 &+ 1
                answer.append(alphabet[Int(seed % UInt64(alphabet.count))])
            }
            let reply = PendingReply(idempotencyKey: "ios-property-\(seed)", questionID: "qst_\(sample)", answer: answer, createdAt: .now)
            try JSONEncoder().encode([reply]).write(to: file)
            let queue = ReplyQueue(fileURL: file) { _ in throw MeowAPIError(status: 404, code: "not_found", message: "Gone") }
            _ = await queue.flush()
            let restarted = ReplyQueue(fileURL: file) { _ in XCTFail("failed generated answer resent"); throw URLError(.badURL) }
            let failures = await restarted.failed
            XCTAssertEqual(failures.first?.answer, answer)
            XCTAssertEqual(failures.first?.idempotencyKey, reply.idempotencyKey)
            XCTAssertEqual(failures.first?.questionID, reply.questionID)
            let retried = await restarted.flush()
            XCTAssertTrue(retried.isEmpty)
        }
    }
}

@MainActor
extension FrontendRecoveryTests {
    func testRefreshIncludesUrgentQuestionBeyondFirstPageAndPaginationDoesNotDuplicateIt() async throws {
        func item(_ id: String, expiry: String? = nil) -> [String: Any] {
            var result: [String: Any] = ["id": id, "type": expiry == nil ? "info" : "question", "title": id,
                "body": "", "source": "Codex", "priority": "normal", "push_status": "accepted",
                "created_at": "2026-10-20T00:00:00Z"]
            if let expiry { result["question"] = ["id": "q_" + id, "status": "pending", "expires_at": expiry] }
            return result
        }
        let later = item("ntf_later", expiry: "2099-01-01T01:00:00Z")
        let urgent = item("ntf_urgent", expiry: "2099-01-01T00:02:00Z")
        let first = [later] + (1..<50).map { item("ntf_info_\($0)") }
        RecoveryURLProtocol.install { request in
            let url = request.url!
            if url.path.hasSuffix("/counts") { return (200, Data(#"{"unread":51,"pending":2}"#.utf8)) }
            let query = URLComponents(url: url, resolvingAgainstBaseURL: false)!.queryItems ?? []
            let pending = query.contains { $0.name == "filter" && $0.value == "pending" }
            let more = query.contains { $0.name == "cursor" }
            let rows = pending ? [later, urgent] : (more ? [urgent] : first)
            let value: [String: Any] = ["notifications": rows, "next_cursor": pending || more ? NSNull() : "older"]
            return (200, try JSONSerialization.data(withJSONObject: value))
        }
        let model = AppModel()
        await model.refresh()
        XCTAssertNil(model.loadError)
        XCTAssertEqual(model.notifications.count, 51)
        XCTAssertEqual(InboxPresentation(model.notifications, now: .now).pending.first?.id, "ntf_urgent")
        XCTAssertNil(model.nextCursor, "all ordinary rows in this fixture are now synchronized locally; there is no remote display cursor")
        await model.loadMore()
        XCTAssertEqual(model.notifications.count, 51)
        XCTAssertNil(model.nextCursor)
    }
}
