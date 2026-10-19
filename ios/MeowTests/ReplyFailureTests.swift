import XCTest
@testable import Meow

private final class ReplyIdentity: @unchecked Sendable {
    private let lock = NSLock()
    private var value: NotificationIdentity
    init(_ value: NotificationIdentity) { self.value = value }
    func current() -> NotificationIdentity { lock.withLock { value } }
    func change(to next: NotificationIdentity) { lock.withLock { value = next } }
}

final class ReplyFailureTests: XCTestCase {
    private let identity = NotificationIdentity(server: URL(string: "https://a.example")!, token: "mdt_a",
                                                deviceID: "dev_a", bindingID: "bind_a")
    private var file: URL { FileManager.default.temporaryDirectory.appending(path: UUID().uuidString + ".json") }
    private func question() -> MeowQuestion {
        MeowQuestion(id: "qst_1", status: .answered, question: "Deploy?", choices: ["approve", "reject"],
                     answer: "approve", createdAt: .now, expiresAt: .now.addingTimeInterval(60),
                     resolvedAt: .now, notificationId: "ntf_1")
    }

    private actor Server {
        var calls: [PendingReply] = []
        var results: [Result<MeowQuestion, Error>]
        init(_ results: [Result<MeowQuestion, Error>]) { self.results = results }
        func send(_ reply: PendingReply) throws -> MeowQuestion {
            calls.append(reply)
            guard !results.isEmpty else { throw URLError(.badServerResponse) }
            return try results.removeFirst().get()
        }
    }

    func testDelayedActionCannotEnterResumedNewServerQueue() async throws {
        let current = ReplyIdentity(identity)
        let path = file
        defer { try? FileManager.default.removeItem(at: path) }
        let server = Server([.success(question())])
        let queue = ReplyQueue(fileURL: path, currentIdentity: { current.current() }, send: { try await server.send($0) })
        // Delegate captured A. Before its call enters the queue, A is discarded
        // and registration B resumes that same actor.
        let captured = current.current()
        try await queue.discardAndPause()
        var next = identity
        next.server = URL(string: "https://b.example")!; next.token = "mdt_b"
        next.deviceID = "dev_b"; next.bindingID = "bind_b"
        current.change(to: next)
        await queue.resume()
        let result = await queue.submit(questionID: "qst_1", answer: "approve", identity: captured)
        guard case .discarded = result else { return XCTFail("an old action must not use the resumed B session") }
        let calls = await server.calls
        let pending = await queue.pending
        XCTAssertTrue(calls.isEmpty)
        XCTAssertTrue(pending.isEmpty)
        let saved = try JSONDecoder().decode([PendingReply].self, from: Data(contentsOf: path))
        XCTAssertTrue(saved.isEmpty)
        // Current B actions remain usable; the fix must not forbid replies.
        guard case .accepted = await queue.submit(questionID: "qst_1", answer: "approve", identity: next) else {
            return XCTFail("the current registration must still be able to answer")
        }
    }

    func testCommittedResponseWithLostAcknowledgmentReplaysSameKeyAfterRestart() async throws {
        let path = file
        defer { try? FileManager.default.removeItem(at: path) }
        let server = Server([.failure(URLError(.networkConnectionLost)), .success(question())])
        let queue = ReplyQueue(fileURL: path) { try await server.send($0) }
        guard case .queued = await queue.submit(questionID: "qst_1", answer: "approve") else {
            return XCTFail("unknown outcome must retain the original submission")
        }
        let restarted = ReplyQueue(fileURL: path) { try await server.send($0) }
        let results = await restarted.flush()
        guard case .accepted = results.first?.1 else { return XCTFail("the replayed server result must be accepted") }
        let calls = await server.calls
        XCTAssertEqual(calls.count, 2)
        XCTAssertEqual(calls[0], calls[1], "every replay must preserve answer and idempotency key")
        let remaining = await restarted.pending
        XCTAssertTrue(remaining.isEmpty)
    }

    func testPermanentHTTPRefusalsStopAndTemporaryFailuresRetainOriginalKey() async throws {
        for status in [401, 404, 409, 410, 429, 500, 503] {
            let path = file
            defer { try? FileManager.default.removeItem(at: path) }
            let server = Server([.failure(MeowAPIError(status: status, code: "test", message: "refused")), .success(question())])
            let queue = ReplyQueue(fileURL: path) { try await server.send($0) }
            let result = await queue.submit(questionID: "qst_1", answer: "approve")
            if status == 429 || status >= 500 {
                guard case .queued = result else { return XCTFail("temporary HTTP \(status) must remain queued") }
                _ = await queue.flush()
                let calls = await server.calls
                XCTAssertEqual(calls.count, 2)
                XCTAssertEqual(calls[0].idempotencyKey, calls[1].idempotencyKey)
            } else {
                guard case .refused = result else { return XCTFail("HTTP \(status) must be terminal") }
                _ = await queue.flush()
                let calls = await server.calls
                XCTAssertEqual(calls.count, 1)
            }
            let pending = await queue.pending
            XCTAssertTrue(pending.isEmpty)
        }
    }

    func testUnwritableQueueNeverSendsAndCorruptFileIsPreserved() async throws {
        let blocker = file
        defer { try? FileManager.default.removeItem(at: blocker) }
        let server = Server([.success(question())])
        let unwritable = ReplyQueue(fileURL: blocker.appending(path: "queue.json")) { try await server.send($0) }
        try Data("corrupt".utf8).write(to: blocker)
        guard case .notSaved = await unwritable.submit(questionID: "qst_1", answer: "approve") else {
            return XCTFail("never claim an unsaved reply is queued")
        }
        let corrupt = ReplyQueue(fileURL: blocker) { try await server.send($0) }
        guard case .notSaved = await corrupt.submit(questionID: "qst_1", answer: "approve") else {
            return XCTFail("never overwrite a corrupt queue")
        }
        XCTAssertEqual(try Data(contentsOf: blocker), Data("corrupt".utf8))
        let calls = await server.calls
        XCTAssertTrue(calls.isEmpty)
    }

    private actor HeldServer {
        let started: [XCTestExpectation]
        var calls: [PendingReply] = []
        var continuations: [CheckedContinuation<MeowQuestion, Error>] = []
        init(_ started: [XCTestExpectation]) { self.started = started }
        func send(_ reply: PendingReply) async throws -> MeowQuestion {
            calls.append(reply)
            return try await withCheckedThrowingContinuation {
                continuations.append($0)
                started[continuations.count - 1].fulfill()
            }
        }
        func finish(_ index: Int, result: Result<MeowQuestion, Error>) { continuations[index].resume(with: result) }
    }

    func testConcurrentDuplicateUsesSameKeyAndConflictingAnswerIsNotSent() async throws {
        let firstStarted = expectation(description: "first send")
        let secondStarted = expectation(description: "duplicate send")
        let held = HeldServer([firstStarted, secondStarted])
        let path = file
        defer { try? FileManager.default.removeItem(at: path) }
        let queue = ReplyQueue(fileURL: path) { try await held.send($0) }
        let first = Task { await queue.submit(questionID: "qst_1", answer: "approve") }
        await fulfillment(of: [firstStarted], timeout: 3)
        let second = Task { await queue.submit(questionID: "qst_1", answer: "approve") }
        await fulfillment(of: [secondStarted], timeout: 3)
        guard case .alreadyQueued(let original) = await queue.submit(questionID: "qst_1", answer: "reject") else {
            return XCTFail("a conflicting answer must not replace the saved answer")
        }
        XCTAssertEqual(original.answer, "approve")
        let calls = await held.calls
        XCTAssertEqual(calls.count, 2)
        XCTAssertEqual(calls[0].idempotencyKey, calls[1].idempotencyKey)
        await held.finish(0, result: .success(question()))
        _ = await first.value
        let canSwitch = await queue.pauseIfIdle()
        XCTAssertFalse(canSwitch, "a duplicate request still in flight belongs to the old session")
        await held.finish(1, result: .success(question()))
        _ = await second.value
        let idle = await queue.pauseIfIdle()
        XCTAssertTrue(idle)
    }

    func testDiscardStopsOldFlushAndIgnoresLateSuccessOrFailure() async throws {
        for result: Result<MeowQuestion, Error> in [.success(question()), .failure(URLError(.timedOut)),
                .failure(MeowAPIError(status: 410, code: "expired", message: "expired"))] {
            let started = expectation(description: "old batch started")
            let held = HeldServer([started])
            let path = file
            defer { try? FileManager.default.removeItem(at: path) }
            let replies = ["qst_1", "qst_2"].map {
                PendingReply(idempotencyKey: UUID().uuidString, questionID: $0, answer: "approve", createdAt: .now)
            }
            try JSONEncoder().encode(replies).write(to: path)
            let queue = ReplyQueue(fileURL: path) { try await held.send($0) }
            let flushing = Task { await queue.flush() }
            await fulfillment(of: [started], timeout: 3)
            try await queue.discardAndPause()
            await queue.resume()
            await held.finish(0, result: result)
            let results = await flushing.value
            XCTAssertEqual(results.count, 1)
            guard case .discarded = results.first?.1 else { return XCTFail("discard must dominate every late result") }
            let calls = await held.calls
            XCTAssertEqual(calls.map(\.questionID), ["qst_1"], "old batch must not send its next item on the new session")
            let durable = try JSONDecoder().decode([PendingReply].self, from: Data(contentsOf: path))
            XCTAssertTrue(durable.isEmpty)
        }
    }

    func testFailedDiscardPreservesDurableAnswerAndSenderCanRetry() async throws {
        let directory = file
        let backup = directory.appendingPathExtension("backup")
        let path = directory.appending(path: "queue.json")
        defer {
            try? FileManager.default.removeItem(at: directory)
            try? FileManager.default.removeItem(at: backup)
        }
        let server = Server([.failure(URLError(.timedOut)), .success(question())])
        let queue = ReplyQueue(fileURL: path) { try await server.send($0) }
        _ = await queue.submit(questionID: "qst_1", answer: "approve")
        try FileManager.default.moveItem(at: directory, to: backup)
        try Data().write(to: directory)
        do { try await queue.discardAndPause(); XCTFail("discard must fail when its durable write fails") }
        catch { /* The old registration must remain usable. */ }
        XCTAssertEqual(try JSONDecoder().decode([PendingReply].self,
                        from: Data(contentsOf: backup.appending(path: "queue.json"))).count, 1)
        try FileManager.default.removeItem(at: directory)
        try FileManager.default.moveItem(at: backup, to: directory)
        let results = await queue.flush()
        guard case .accepted = results.first?.1 else { return XCTFail("failed discard must not pause the old sender") }
    }
}
