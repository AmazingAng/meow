import XCTest
@testable import Meow

final class ModelTests: XCTestCase {
    func testParsesGoTimestamps() throws {
        let cases: [(String, Double)] = [
            ("2026-10-17T06:28:11Z", 0),
            ("2026-10-17T06:28:11.1Z", 0.1),
            ("2026-10-17T06:28:11.123Z", 0.123),
            ("2026-10-17T06:28:11.123456789Z", 0.123456789),
            ("2026-10-17T14:28:11.5+08:00", 0.5),
        ]
        let base = try XCTUnwrap(MeowJSON.parseDate("2026-10-17T06:28:11Z"))
        for (s, fraction) in cases {
            let d = try XCTUnwrap(MeowJSON.parseDate(s), s)
            XCTAssertEqual(d.timeIntervalSince(base), fraction, accuracy: 1e-6, s)
        }
        XCTAssertNil(MeowJSON.parseDate("yesterday"))
    }

    func testDecodesInboxPage() throws {
        let json = """
        {"notifications":[{"id":"ntf_1","type":"question","title":"Deploy to production?","body":"services/api",
        "source":"Claude Code","priority":"high","push_status":"accepted","created_at":"2026-10-17T06:28:11.12Z",
        "delivered_at":null,"read_at":null,"question":{"id":"qst_1","status":"pending","choices":["approve","reject"],
        "answer":null,"expires_at":"2026-10-17T06:43:11.12Z"}}],"next_cursor":null}
        """
        let page = try MeowJSON.decoder.decode(InboxPage.self, from: Data(json.utf8))
        let n = try XCTUnwrap(page.notifications.first)
        XCTAssertEqual(n.type, .question)
        XCTAssertTrue(n.isUnread)
        XCTAssertEqual(n.question?.isApproval, true)
        XCTAssertNil(page.nextCursor)
    }

    func testQuestionOpenAndApproval() {
        let now = Date()
        var q = MeowNotification.Question(id: "q", status: .pending, choices: ["Reject", "APPROVE"], answer: nil,
                                          expiresAt: now.addingTimeInterval(60))
        XCTAssertTrue(q.isOpen(at: now))
        XCTAssertTrue(q.isApproval)
        XCTAssertFalse(q.isOpen(at: now.addingTimeInterval(61)))
        q.choices = ["postgres", "sqlite"]
        XCTAssertFalse(q.isApproval)
        q.choices = nil
        XCTAssertFalse(q.isApproval)
        q.status = .answered
        XCTAssertFalse(q.isOpen(at: now))
    }

    func testAvatarInitialsAndStableColor() {
        XCTAssertEqual(AvatarStyle.initials(for: "Claude Code"), "CC")
        XCTAssertEqual(AvatarStyle.initials(for: "codex"), "C")
        XCTAssertEqual(AvatarStyle.initials(for: "github-actions"), "GA")
        XCTAssertEqual(AvatarStyle.initials(for: "部署机器人"), "部")
        XCTAssertEqual(AvatarStyle.initials(for: ""), "")
        XCTAssertEqual(AvatarStyle.colorIndex(for: "Codex"), AvatarStyle.colorIndex(for: "codex"))
        XCTAssertTrue((0..<AvatarStyle.palette.count).contains(AvatarStyle.colorIndex(for: "Claude Code")))
    }

    func testCountdownFormat() {
        XCTAssertEqual(ExpiryCountdown.format(65), "1:05")
        XCTAssertEqual(ExpiryCountdown.format(3725), "1:02:05")
    }
}

final class ReplyQueueTests: XCTestCase {
    /// Records calls and replays scripted results.
    actor FakeServer {
        var results: [Result<MeowQuestion, Error>] = []
        var calls: [PendingReply] = []

        func script(_ r: [Result<MeowQuestion, Error>]) { results = r }

        func send(_ reply: PendingReply) throws -> MeowQuestion {
            calls.append(reply)
            return try results.removeFirst().get()
        }
    }

    private func question(_ status: QuestionStatus, answer: String?) -> MeowQuestion {
        MeowQuestion(id: "qst_1", status: status, question: "Deploy?", choices: ["approve", "reject"], answer: answer,
                     createdAt: .now, expiresAt: .now.addingTimeInterval(60), resolvedAt: nil, notificationId: "ntf_1")
    }

    private func makeQueue(_ server: FakeServer, file: URL) -> ReplyQueue {
        ReplyQueue(fileURL: file) { try await server.send($0) }
    }

    private var tempFile: URL {
        FileManager.default.temporaryDirectory.appending(path: "replies-\(UUID().uuidString).json")
    }

    func testOfflineReplyIsPersistedAndRetriedWithSameKey() async throws {
        let server = FakeServer()
        let file = tempFile
        await server.script([.failure(URLError(.notConnectedToInternet))])

        let queue = makeQueue(server, file: file)
        guard case .queued = await queue.submit(questionID: "qst_1", answer: "approve") else {
            return XCTFail("expected the reply to be queued")
        }

        // A new queue (as after the app was killed) loads it from disk and resends it.
        await server.script([.success(question(.answered, answer: "approve"))])
        let reloaded = makeQueue(server, file: file)
        let results = await reloaded.flush()
        XCTAssertEqual(results.count, 1)
        guard case .accepted(let q) = results[0].1 else { return XCTFail("expected acceptance") }
        XCTAssertEqual(q.answer, "approve")

        let calls = await server.calls
        XCTAssertEqual(calls.count, 2)
        XCTAssertEqual(calls[0].idempotencyKey, calls[1].idempotencyKey)
        let remaining = await reloaded.pending
        XCTAssertTrue(remaining.isEmpty)
    }

    func testRefusalIsNotRetried() async {
        let server = FakeServer()
        await server.script([.failure(MeowAPIError(status: 410, code: "expired", message: "expired"))])
        let queue = makeQueue(server, file: tempFile)
        guard case .refused(let e) = await queue.submit(questionID: "qst_1", answer: "approve") else {
            return XCTFail("expected refusal")
        }
        XCTAssertEqual(e.code, "expired")
        let remaining = await queue.pending
        XCTAssertTrue(remaining.isEmpty)
    }

    func testServerErrorStaysQueued() async {
        let server = FakeServer()
        await server.script([.failure(MeowAPIError(status: 503, code: "unavailable", message: ""))])
        let queue = makeQueue(server, file: tempFile)
        guard case .queued = await queue.submit(questionID: "qst_1", answer: "approve") else {
            return XCTFail("expected the reply to stay queued")
        }
    }

    func testDifferentAnswerDoesNotReplaceQueuedOne() async {
        let server = FakeServer()
        await server.script([.failure(URLError(.timedOut))])
        let queue = makeQueue(server, file: tempFile)
        _ = await queue.submit(questionID: "qst_1", answer: "approve")
        guard case .alreadyQueued(let existing) = await queue.submit(questionID: "qst_1", answer: "reject") else {
            return XCTFail("expected the first answer to be kept")
        }
        XCTAssertEqual(existing.answer, "approve")
        let calls = await server.calls
        XCTAssertEqual(calls.count, 1, "the conflicting answer must not be sent")
    }
}

extension ReplyQueueTests {
    func testUnwritableQueueDoesNotSendOrClaimPersistence() async throws {
        let server = FakeServer()
        await server.script([.failure(URLError(.notConnectedToInternet))])
        // A file cannot also be the directory containing the queue.
        let blocker = tempFile
        defer { try? FileManager.default.removeItem(at: blocker) }
        let queue = makeQueue(server, file: blocker.appending(path: "replies.json"))
        try Data().write(to: blocker)
        let outcome = await queue.submit(questionID: "qst_1", answer: "approve")
        if case .queued = outcome { XCTFail("must not claim an unsaved reply is queued") }
        if case .accepted = outcome { XCTFail("must persist before contacting the server") }
        let pending = await queue.pending
        let calls = await server.calls
        XCTAssertTrue(pending.isEmpty, "unsaved replies must not enter the durable queue")
        XCTAssertTrue(calls.isEmpty, "must not send until the idempotency key has been saved")
    }
}

/// Holds network responses at the URLSession boundary, never replaces AppModel logic.
private final class ReviewURLProtocol: URLProtocol, @unchecked Sendable {
    private static let lock = NSLock()
    private static var callback: ((URLRequest) -> (Int, Data))?

    static func install(_ handler: @escaping (URLRequest) -> (Int, Data)) {
        lock.withLock { callback = handler }
        URLProtocol.registerClass(Self.self)
    }
    static func uninstall() {
        URLProtocol.unregisterClass(Self.self)
        lock.withLock { callback = nil }
    }
    override class func canInit(with request: URLRequest) -> Bool {
        request.url?.host?.hasSuffix(".meow-review.invalid") == true
    }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        guard let handler = Self.lock.withLock({ Self.callback }) else {
            client?.urlProtocol(self, didFailWithError: URLError(.cancelled))
            return
        }
        let (status, data) = handler(request)
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil,
                                       headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@MainActor
final class SessionRegressionTests: XCTestCase {
    private let serverA = URL(string: "https://a.meow-review.invalid")!
    private let serverB = URL(string: "https://b.meow-review.invalid")!

    private struct Snapshot {
        let server = MeowShared.server
        let token = MeowShared.deviceToken
        let onboarded = MeowShared.defaults.bool(forKey: "onboarded")
        let key = Keychain.get("device_key")
        let keyID = MeowShared.defaults.string(forKey: "device_key_id")
        func restore() {
            MeowShared.server = server
            MeowShared.deviceToken = token
            MeowShared.defaults.set(onboarded, forKey: "onboarded")
            Keychain.set(key, for: "device_key")
            MeowShared.defaults.set(keyID, forKey: "device_key_id")
        }
    }

    private var registrationJSON: Data {
        Data("""
        {"device":{"id":"dev_review","name":"Test","apns_env":"production","has_apns_token":false,
        "created_at":"2026-10-17T00:00:00Z","last_seen_at":"2026-10-17T00:00:00Z"},
        "device_token":"mdt_review_A","key":{"id":"key_review","name":"Default","hint":"meow_…",
        "scopes":["notify","ask"],"created_at":"2026-10-17T00:00:00Z","last_used_at":null,"secret":"meow_review"}}
        """.utf8)
    }

    func testServerSwitchPreservesPendingReplyAndIdentity() async {
        let snapshot = Snapshot()
        defer { snapshot.restore() }
        MeowShared.server = serverA
        MeowShared.deviceToken = "mdt_review_A"
        let file = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: file) }
        let queue = ReplyQueue(fileURL: file) { _ in throw URLError(.notConnectedToInternet) }
        _ = await queue.submit(questionID: "qst_review", answer: "approve")
        let model = AppModel(replies: queue)
        await model.reset(server: serverB)
        // Also lets the old unstructured removeAll task run on the baseline.
        try? await Task.sleep(for: .milliseconds(50))
        let pending = await queue.pending
        XCTAssertEqual(model.server, serverA)
        XCTAssertTrue(model.isRegistered)
        XCTAssertEqual(MeowShared.deviceToken, "mdt_review_A")
        XCTAssertEqual(pending.count, 1, "switching must not delete an offline answer")
        XCTAssertNotNil(model.alert, "explain that pending replies prevent switching")
    }

    func testServerCannotChangeDuringRegistration() async {
        let snapshot = Snapshot()
        defer { snapshot.restore(); ReviewURLProtocol.uninstall() }
        MeowShared.server = serverA
        MeowShared.deviceToken = nil
        let started = expectation(description: "registration started")
        let gate = DispatchSemaphore(value: 0)
        let json = registrationJSON
        ReviewURLProtocol.install { _ in
            started.fulfill()
            _ = gate.wait(timeout: .now() + 10)
            return (201, json)
        }
        let model = AppModel()
        let registration = Task { await model.register() }
        await fulfillment(of: [started], timeout: 5)
        XCTAssertTrue(model.isRegistering)
        model.setServer(serverB)
        XCTAssertEqual(model.server, serverA, "a live registration cannot change its destination")
        gate.signal()
        await registration.value
        XCTAssertTrue(model.isRegistered)
        XCTAssertEqual(MeowShared.server, serverA)
        XCTAssertEqual(MeowShared.deviceToken, "mdt_review_A")
    }

    func testOldInboxResponseCannotRepopulateAfterSwitch() async {
        let snapshot = Snapshot()
        defer { snapshot.restore(); ReviewURLProtocol.uninstall() }
        MeowShared.server = serverA
        MeowShared.deviceToken = "mdt_review_A"
        let started = expectation(description: "old inbox requested")
        let gate = DispatchSemaphore(value: 0)
        ReviewURLProtocol.install { req in
            if req.url!.path == "/v1/inbox" {
                started.fulfill()
                _ = gate.wait(timeout: .now() + 10)
                return (200, Data("{\"notifications\":[],\"next_cursor\":null}".utf8))
            }
            return (200, Data("{\"unread\":7,\"pending\":2}".utf8))
        }
        let file = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        let queue = ReplyQueue(fileURL: file) { _ in throw URLError(.notConnectedToInternet) }
        let model = AppModel(replies: queue)
        let refresh = Task { await model.refresh() }
        await fulfillment(of: [started], timeout: 5)
        await model.reset(server: serverB)
        gate.signal()
        await refresh.value
        XCTAssertEqual(model.server, serverB)
        XCTAssertFalse(model.hasLoaded, "a response for the old identity must be ignored")
        XCTAssertEqual(model.counts.unread, 0)
    }
}

extension ReplyQueueTests {
    func testPausedQueueCannotSendUntilResumed() async throws {
        let server = FakeServer()
        await server.script([.failure(URLError(.notConnectedToInternet))])
        let file = tempFile
        defer { try? FileManager.default.removeItem(at: file) }
        let queue = makeQueue(server, file: file)
        let paused = await queue.pauseIfIdle()
        XCTAssertTrue(paused)
        guard case .notSaved = await queue.submit(questionID: "qst_1", answer: "approve") else {
            return XCTFail("a paused queue must reject new submissions")
        }
        let calls = await server.calls
        let pending = await queue.pending
        XCTAssertTrue(calls.isEmpty)
        XCTAssertTrue(pending.isEmpty)
        await queue.resume()
        guard case .queued = await queue.submit(questionID: "qst_1", answer: "approve") else {
            return XCTFail("registration must be able to resume the queue")
        }
    }

    func testCorruptQueueIsNotSilentlyOverwritten() async throws {
        let server = FakeServer()
        let file = tempFile
        defer { try? FileManager.default.removeItem(at: file) }
        let corrupt = Data("not valid queue JSON".utf8)
        try corrupt.write(to: file)
        let queue = makeQueue(server, file: file)
        guard case .notSaved = await queue.submit(questionID: "qst_1", answer: "approve") else {
            return XCTFail("unreadable saved replies must fail closed")
        }
        let canSwitch = await queue.pauseIfIdle()
        let calls = await server.calls
        XCTAssertFalse(canSwitch)
        XCTAssertTrue(calls.isEmpty)
        XCTAssertEqual(try Data(contentsOf: file), corrupt)
    }

    func testFailedQueueCleanupRetainsKeyUntilItCanBePersisted() async throws {
        let server = FakeServer()
        let directory = tempFile
        let backup = directory.appendingPathExtension("backup")
        let file = directory.appending(path: "queue.json")
        defer {
            try? FileManager.default.removeItem(at: directory)
            try? FileManager.default.removeItem(at: backup)
        }
        let queue = makeQueue(server, file: file)
        await server.script([.failure(URLError(.notConnectedToInternet))])
        _ = await queue.submit(questionID: "qst_1", answer: "approve")
        // Make the path unwritable while preserving the original durable file.
        try FileManager.default.moveItem(at: directory, to: backup)
        try Data().write(to: directory)
        await server.script([.success(question(.answered, answer: "approve"))])
        let first = await queue.flush()
        guard case .accepted = first.first?.1 else { return XCTFail("the server result remains authoritative") }
        let retained = await queue.pending
        let canSwitch = await queue.pauseIfIdle()
        XCTAssertEqual(retained.count, 1)
        XCTAssertFalse(canSwitch)
        try FileManager.default.removeItem(at: directory)
        try FileManager.default.moveItem(at: backup, to: directory)
        await server.script([.success(question(.answered, answer: "approve"))])
        _ = await queue.flush()
        let calls = await server.calls
        XCTAssertEqual(Set(calls.map(\.idempotencyKey)).count, 1)
        let reloaded = makeQueue(server, file: file)
        let remaining = await reloaded.pending
        XCTAssertTrue(remaining.isEmpty)
    }

    actor HeldServer {
        var continuations: [CheckedContinuation<MeowQuestion, Error>] = []
        let started: [XCTestExpectation]
        init(started: [XCTestExpectation]) { self.started = started }
        func send(_ reply: PendingReply) async throws -> MeowQuestion {
            try await withCheckedThrowingContinuation { continuation in
                let index = continuations.count
                continuations.append(continuation)
                started[index].fulfill()
            }
        }
        func finish(_ index: Int, with question: MeowQuestion) { continuations[index].resume(returning: question) }
    }

    func testSwitchCannotPauseWhileDuplicateSendIsStillInFlight() async throws {
        let firstStarted = expectation(description: "first request in flight")
        let secondStarted = expectation(description: "second request in flight")
        let server = HeldServer(started: [firstStarted, secondStarted])
        let file = tempFile
        defer { try? FileManager.default.removeItem(at: file) }
        let queue = ReplyQueue(fileURL: file) { try await server.send($0) }
        let first = Task { await queue.submit(questionID: "qst_1", answer: "approve") }
        await fulfillment(of: [firstStarted], timeout: 5)
        let second = Task { await queue.submit(questionID: "qst_1", answer: "approve") }
        await fulfillment(of: [secondStarted], timeout: 5)
        await server.finish(0, with: question(.answered, answer: "approve"))
        _ = await first.value
        let pending = await queue.pending
        XCTAssertTrue(pending.isEmpty)
        let canSwitch = await queue.pauseIfIdle()
        XCTAssertFalse(canSwitch, "the other HTTP request still belongs to this identity")
        await server.finish(1, with: question(.answered, answer: "approve"))
        _ = await first.value
        _ = await second.value
        let idle = await queue.pauseIfIdle()
        XCTAssertTrue(idle)
    }
}

extension SessionRegressionTests {
    func testFinishOnboardingLoadsInboxWithoutSceneTransition() async {
        let snapshot = Snapshot()
        defer { snapshot.restore(); ReviewURLProtocol.uninstall() }
        MeowShared.server = serverA
        MeowShared.deviceToken = "mdt_review_A"
        ReviewURLProtocol.install { req in
            if req.url!.path == "/v1/inbox" {
                return (200, Data("{\"notifications\":[],\"next_cursor\":null}".utf8))
            }
            return (200, Data("{\"unread\":0,\"pending\":0}".utf8))
        }
        let model = AppModel()
        await model.finishOnboarding()
        XCTAssertTrue(model.hasFinishedOnboarding)
        XCTAssertTrue(model.hasLoaded)
        XCTAssertNil(model.loadError)
    }
}


extension SessionRegressionTests {
    func testConfirmedDiscardAllowsSwitchWithOfflineReplies() async {
        let snapshot = Snapshot()
        defer { snapshot.restore() }
        MeowShared.server = serverA
        MeowShared.deviceToken = "mdt_review_A"
        let file = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: file) }
        let queue = ReplyQueue(fileURL: file) { _ in throw URLError(.notConnectedToInternet) }
        _ = await queue.submit(questionID: "qst_old", answer: "approve")
        let model = AppModel(replies: queue)
        let switched = await model.reset(server: serverB, discardPendingReplies: true)
        XCTAssertTrue(switched)
        XCTAssertEqual(model.server, serverB)
        XCTAssertFalse(model.isRegistered)
        XCTAssertNil(MeowShared.deviceToken)
        let reloaded = ReplyQueue(fileURL: file) { _ in throw URLError(.notConnectedToInternet) }
        let pending = await reloaded.pending
        XCTAssertTrue(pending.isEmpty, "discard must survive relaunch")
    }

    func testFailedDiscardPreservesServerAndDurableReply() async throws {
        let snapshot = Snapshot()
        defer { snapshot.restore() }
        MeowShared.server = serverA
        MeowShared.deviceToken = "mdt_review_A"
        let directory = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        let backup = directory.appendingPathExtension("backup")
        let file = directory.appending(path: "queue.json")
        defer {
            try? FileManager.default.removeItem(at: directory)
            try? FileManager.default.removeItem(at: backup)
        }
        let queue = ReplyQueue(fileURL: file) { _ in throw URLError(.notConnectedToInternet) }
        _ = await queue.submit(questionID: "qst_old", answer: "approve")
        try FileManager.default.moveItem(at: directory, to: backup)
        try Data().write(to: directory)
        let model = AppModel(replies: queue)
        let switched = await model.reset(server: serverB, discardPendingReplies: true)
        XCTAssertFalse(switched)
        XCTAssertEqual(model.server, serverA)
        XCTAssertEqual(MeowShared.deviceToken, "mdt_review_A")
        XCTAssertNotNil(model.alert)
        let reloaded = ReplyQueue(fileURL: backup.appending(path: "queue.json")) { _ in throw URLError(.notConnectedToInternet) }
        let pending = await reloaded.pending
        XCTAssertEqual(pending.map(\.questionID), ["qst_old"])
    }

    func testDiscardDoesNotWaitForOrAcceptALateResponse() async {
        let snapshot = Snapshot()
        defer { snapshot.restore() }
        MeowShared.server = serverA
        MeowShared.deviceToken = "mdt_review_A"
        let started = expectation(description: "old reply in flight")
        let held = ReplyQueueTests.HeldServer(started: [started])
        let file = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: file) }
        let queue = ReplyQueue(fileURL: file) { reply in
            if reply.questionID == "qst_old" { return try await held.send(reply) }
            throw URLError(.notConnectedToInternet)
        }
        let sending = Task { await queue.submit(questionID: "qst_old", answer: "approve") }
        await fulfillment(of: [started], timeout: 5)
        let model = AppModel(replies: queue)
        let switched = await model.reset(server: serverB, discardPendingReplies: true)
        XCTAssertTrue(switched, "switch must not wait for a sender that ignores cancellation")
        await queue.resume()
        _ = await queue.submit(questionID: "qst_new", answer: "reject")
        let question = MeowQuestion(id: "qst_old", status: .answered, question: "Approve?",
            choices: ["approve", "reject"], answer: "approve", createdAt: .now,
            expiresAt: .now.addingTimeInterval(60), resolvedAt: .now, notificationId: "ntf_old")
        await held.finish(0, with: question)
        let outcome = await sending.value
        if case .accepted = outcome { XCTFail("old result must not be accepted in the new session") }
        if case .queued = outcome { XCTFail("discarded reply must not be requeued") }
        let pending = await queue.pending
        XCTAssertEqual(pending.map(\.questionID), ["qst_new"])
    }
}

extension ReplyQueueTests {
    actor FlushRecorder {
        let held: HeldServer
        var calls: [String] = []
        init(held: HeldServer) { self.held = held }
        func send(_ reply: PendingReply) async throws -> MeowQuestion {
            calls.append(reply.questionID)
            if calls.count == 1 { return try await held.send(reply) }
            throw URLError(.notConnectedToInternet)
        }
    }

    func testDiscardCancelsSendAndStopsOldFlushAfterResume() async throws {
        let started = expectation(description: "old flush started")
        let cancelled = expectation(description: "sender cancellation requested")
        let held = HeldServer(started: [started])
        let recorder = FlushRecorder(held: held)
        let file = tempFile
        defer { try? FileManager.default.removeItem(at: file) }
        let originals = ["qst_1", "qst_2"].map {
            PendingReply(idempotencyKey: UUID().uuidString, questionID: $0, answer: "approve", createdAt: .now)
        }
        try JSONEncoder().encode(originals).write(to: file)
        let queue = ReplyQueue(fileURL: file) { reply in
            try await withTaskCancellationHandler {
                try await recorder.send(reply)
            } onCancel: { cancelled.fulfill() }
        }
        let flushing = Task { await queue.flush() }
        await fulfillment(of: [started], timeout: 5)
        try await queue.discardAndPause()
        await fulfillment(of: [cancelled], timeout: 5)
        await queue.resume()
        await held.finish(0, with: question(.answered, answer: "approve"))
        let results = await flushing.value
        let calls = await recorder.calls
        let pending = await queue.pending
        XCTAssertEqual(calls, ["qst_1"], "old batch must not send its second reply using the new session")
        XCTAssertEqual(results.count, 1)
        guard case .discarded = results.first?.1 else { return XCTFail("late result must be discarded") }
        XCTAssertTrue(pending.isEmpty)
    }
}

extension ReplyQueueTests.HeldServer {
    func fail(_ index: Int, with error: Error) { continuations[index].resume(throwing: error) }
}

extension ReplyQueueTests {
    func testLateNetworkAndPermanentErrorsStayDiscarded() async throws {
        let errors: [Error] = [URLError(.timedOut), MeowAPIError(status: 410, code: "expired", message: "Expired")]
        for error in errors {
            let started = expectation(description: "request started")
            let held = HeldServer(started: [started])
            let file = tempFile
            defer { try? FileManager.default.removeItem(at: file) }
            let queue = ReplyQueue(fileURL: file) { try await held.send($0) }
            let sending = Task { await queue.submit(questionID: "qst_1", answer: "approve") }
            await fulfillment(of: [started], timeout: 5)
            try await queue.discardAndPause()
            await queue.resume()
            await held.fail(0, with: error)
            let outcome = await sending.value
            guard case .discarded = outcome else { return XCTFail("discard must dominate a late network outcome") }
            let pending = await queue.pending
            let reloaded = ReplyQueue(fileURL: file) { _ in throw URLError(.notConnectedToInternet) }
            let durable = await reloaded.pending
            XCTAssertTrue(pending.isEmpty)
            XCTAssertTrue(durable.isEmpty)
        }
    }
}

final class OnboardingCommandTests: XCTestCase {
    func testSetupCommandAlwaysNamesTheServer() throws {
        let server = try XCTUnwrap(URL(string: "https://meow.example.com/"))
        XCTAssertEqual(AppModel.setupCommand(server: server), "meow setup --server https://meow.example.com")
        let local = try XCTUnwrap(URL(string: "http://127.0.0.1:8080"))
        XCTAssertEqual(AppModel.setupCommand(server: local), "meow setup --server http://127.0.0.1:8080")
    }

    func testSetupCommandQuotesUnsafeURLs() throws {
        let quote = try XCTUnwrap(URL(string: "https://meow.example.com/o'neil"))
        XCTAssertEqual(AppModel.setupCommand(server: quote), #"meow setup --server 'https://meow.example.com/o'\''neil'"#)
        // A typed space is percent-encoded by URL parsing and needs no quoting.
        let space = try XCTUnwrap(MeowShared.serverURL(from: "https://meow.example.com/my meow"))
        XCTAssertEqual(AppModel.setupCommand(server: space), "meow setup --server https://meow.example.com/my%20meow")
        XCTAssertEqual(AppModel.shellQuoted("a b"), "'a b'")
        XCTAssertEqual(AppModel.shellQuoted("$(rm -rf ~)"), "'$(rm -rf ~)'")
        XCTAssertEqual(AppModel.shellQuoted(""), "''")
        XCTAssertFalse(AppModel.setupCommand(server: quote).contains("meow_"), "the Device Key never goes in the command")
    }

    func testParsesServerAddresses() {
        XCTAssertNil(MeowShared.serverURL(from: nil))
        XCTAssertNil(MeowShared.serverURL(from: ""))
        XCTAssertNil(MeowShared.serverURL(from: "   "))
        XCTAssertNil(MeowShared.serverURL(from: "localhost"))
        XCTAssertNil(MeowShared.serverURL(from: "meow.example.com"))
        XCTAssertNil(MeowShared.serverURL(from: "ftp://meow.example.com"))
        XCTAssertEqual(MeowShared.serverURL(from: "  https://meow.example.com/// \n")?.absoluteString, "https://meow.example.com")
        XCTAssertEqual(MeowShared.serverURL(from: "HTTPS://meow.example.com:8443/meow/?q=1#f")?.absoluteString,
                       "HTTPS://meow.example.com:8443/meow")
    }

    func testSelfHostedBuildDoesNotAssumeAServer() {
        // A self-hosted build starts without an assumed server address.
        XCTAssertNil(MeowShared.defaultServer)
    }
}

extension SessionRegressionTests {
    func testSavedServerOutlivesEmptyDefault() {
        let snapshot = Snapshot()
        defer { snapshot.restore() }
        MeowShared.server = serverA
        XCTAssertEqual(MeowShared.server, serverA, "an existing install keeps its server")
        XCTAssertEqual(AppModel().server, serverA)
        MeowShared.server = nil
        XCTAssertEqual(MeowShared.server, MeowShared.defaultServer)
    }

    func testHealthCheckExplainsWhatIsWrong() async {
        defer { ReviewURLProtocol.uninstall() }
        var reply = (200, Data(#"{"status":"ok"}"#.utf8))
        ReviewURLProtocol.install { req in
            XCTAssertEqual(req.url?.path, "/healthz")
            return reply
        }
        let ok = await AppModel.checkServer(" https://a.meow-review.invalid/ ")
        XCTAssertEqual(ok, .success(serverA))
        reply = (200, Data("<html>hello</html>".utf8))
        let html = await AppModel.checkServer("https://a.meow-review.invalid")
        XCTAssertEqual(html, .failure(.notMeow(host: "a.meow-review.invalid")))
        reply = (404, Data())
        let missing = await AppModel.checkServer("https://a.meow-review.invalid")
        XCTAssertEqual(missing, .failure(.notMeow(host: "a.meow-review.invalid")))
        reply = (503, Data(#"{"error":{"code":"unavailable","message":"database unavailable"}}"#.utf8))
        let down = await AppModel.checkServer("https://a.meow-review.invalid")
        XCTAssertEqual(down, .failure(.unreachable(host: "a.meow-review.invalid")))
        let typo = await AppModel.checkServer("meow.example.com")
        XCTAssertEqual(typo, .failure(.invalidAddress))
    }
}
