import Foundation

/// An answer saved on the device until the server accepts or refuses it.
struct PendingReply: Codable, Hashable, Sendable {
    struct Failure: Codable, Hashable, Sendable {
        var status: Int
        var code: String
        var message: String
        var error: MeowAPIError { MeowAPIError(status: status, code: code, message: message) }
    }
    var idempotencyKey: String
    var questionID: String
    var answer: String
    var createdAt: Date
    // Optional for compatibility with queues saved by earlier releases.
    var failure: Failure? = nil
}

enum ReplyOutcome: Sendable {
    /// The server recorded the answer (or had already recorded this exact submission).
    case accepted(MeowQuestion)
    /// The server will never accept it: already resolved, expired, invalid, or not found.
    case refused(MeowAPIError)
    /// The reply could not be saved, so it was not sent.
    case notSaved(String)
    /// A network or server problem; the reply stays queued and is retried.
    case queued(PendingReply)
    /// A different answer to the same question is already waiting to be sent.
    case alreadyQueued(PendingReply)
    /// Explicitly discarded during a server switch; late network results are ignored.
    case discarded
}

/// Persists answers before sending them, so a reply given offline or from the
/// lock screen survives the app being suspended or killed. Every retry reuses
/// the same Idempotency-Key, so the server applies an answer at most once.
actor ReplyQueue {
    typealias Sender = @Sendable (PendingReply) async throws -> MeowQuestion

    static let shared = ReplyQueue(
        fileURL: MeowShared.containerURL.appending(path: "pending-replies.json"),
        makeSender: {
            // Capture the identity while serialized with discardAndPause(), before
            // starting a task that may not run until after the switch.
            let server = MeowShared.server
            let token = MeowShared.deviceToken
            return { reply in
                guard let token, let server else {
                    throw MeowAPIError(status: 401, code: "unauthorized", message: "This device is not registered.")
                }
                return try await MeowAPI(server: server, token: token)
                    .respond(questionID: reply.questionID, answer: reply.answer, idempotencyKey: reply.idempotencyKey)
            }
        }
    )

    private let fileURL: URL
    private let makeSender: @Sendable () -> Sender
    private let currentIdentity: @Sendable () -> NotificationIdentity?
    private var items: [PendingReply]
    private var storageError: String?
    private var paused = false
    private var epoch = 0
    private var active: [UUID: (questionID: String, task: Task<MeowQuestion, Error>)] = [:]
    private var inFlight: Int { active.count }

    init(fileURL: URL, currentIdentity: @escaping @Sendable () -> NotificationIdentity? = { NotificationIdentity.current }, send: @escaping Sender) {
        self.init(fileURL: fileURL, currentIdentity: currentIdentity, makeSender: { send })
    }

    init(fileURL: URL, currentIdentity: @escaping @Sendable () -> NotificationIdentity? = { NotificationIdentity.current }, makeSender: @escaping @Sendable () -> Sender) {
        self.fileURL = fileURL
        self.makeSender = makeSender
        self.currentIdentity = currentIdentity
        do {
            items = try JSONDecoder().decode([PendingReply].self, from: Data(contentsOf: fileURL))
        } catch CocoaError.fileReadNoSuchFile {
            items = []
        } catch {
            items = []
            storageError = Self.storageMessage(error)
        }
    }

    var pending: [PendingReply] { items.filter { $0.failure == nil } }
    var failed: [PendingReply] { items.filter { $0.failure != nil } }

    var outstandingCount: Int {
        Set(pending.map(\.questionID) + active.values.map(\.questionID)).count
    }

    func submit(questionID: String, answer: String, identity: NotificationIdentity? = nil) async -> ReplyOutcome {
        // A delegate/AppModel check precedes an actor hop. Recheck here before
        // persistence or sender capture, serialized with discardAndPause().
        if let identity, currentIdentity() != identity { return .discarded }
        if let storageError { return .notSaved(storageError) }
        guard !paused else { return .notSaved(String(localized: "The server is changing. Please try again afterward.")) }
        if let existing = pending.first(where: { $0.questionID == questionID }) {
            // Same answer again is just a retry; a different one must not replace it.
            return existing.answer == answer ? await deliver(existing) : .alreadyQueued(existing)
        }
        let reply = PendingReply(
            idempotencyKey: "ios-" + UUID().uuidString.lowercased(),
            questionID: questionID, answer: answer, createdAt: .now)
        do {
            try save(items + [reply])
        } catch {
            return .notSaved(Self.storageMessage(error))
        }
        items.append(reply)
        return await deliver(reply)
    }

    /// Retries every queued reply once.
    func flush() async -> [(PendingReply, ReplyOutcome)] {
        var results: [(PendingReply, ReplyOutcome)] = []
        let batchEpoch = epoch
        for reply in pending {
            guard !paused, epoch == batchEpoch else { break }
            results.append((reply, await deliver(reply)))
        }
        return results
    }

    /// The default switch preserves saved and in-flight answers. Discarding
    /// requires the separate, explicitly confirmed path below.
    func pauseIfIdle() -> Bool {
        guard pending.isEmpty, inFlight == 0, storageError == nil else { return false }
        // Failed records belong to the old registration, too.
        if !items.isEmpty {
            do { try save([]); items = [] }
            catch { return false }
        }
        paused = true
        return true
    }

    /// Persist first: a failed write must leave the old session usable.
    /// Cancellation cannot undo an answer the server already processed.
    func discardAndPause() throws {
        try save([])
        paused = true
        epoch += 1
        for entry in active.values { entry.task.cancel() }
        active.removeAll()
        items = []
        storageError = nil
    }

    func resume() { paused = false }

    func dismissFailure(_ idempotencyKey: String) throws {
        let remaining = items.filter { $0.idempotencyKey != idempotencyKey || $0.failure == nil }
        try save(remaining)
        items = remaining
    }

    private func deliver(_ reply: PendingReply) async -> ReplyOutcome {
        guard !paused else { return .notSaved(String(localized: "The server is changing. Please try again afterward.")) }
        let deliveryEpoch = epoch
        let send = makeSender()
        let id = UUID()
        let task = Task {
            try Task.checkCancellation()
            return try await send(reply)
        }
        active[id] = (reply.questionID, task)
        defer { active[id] = nil }
        do {
            let question = try await task.value
            guard deliveryEpoch == epoch else { return .discarded }
            remove(reply)
            return .accepted(question)
        } catch let error as MeowAPIError where !error.isTemporary {
            guard deliveryEpoch == epoch else { return .discarded }
            // Another concurrent delivery may already have accepted this key.
            guard items.contains(where: { $0.idempotencyKey == reply.idempotencyKey && $0.failure == nil }) else {
                return .discarded
            }
            var failed = reply
            failed.failure = .init(status: error.status, code: error.code, message: error.message)
            let remaining = items.filter { $0.idempotencyKey != reply.idempotencyKey &&
                !($0.questionID == reply.questionID && $0.failure != nil) } + [failed]
            do {
                try save(remaining)
                items = remaining
            } catch {
                // Keep the original durable entry and key if archiving fails.
                return .notSaved(String(localized: "Couldn't save the reply failure. Your original answer is still saved. (\(error.localizedDescription))"))
            }
            return .refused(error)
        } catch {
            guard deliveryEpoch == epoch else { return .discarded }
            return .queued(reply)
        }
    }

    private func remove(_ reply: PendingReply) {
        let remaining = items.filter { $0.idempotencyKey != reply.idempotencyKey &&
            !($0.questionID == reply.questionID && $0.failure != nil) }
        // If cleanup fails, retain the durable entry and its key. A retry can
        // safely replay the server result and try cleanup again.
        do {
            try save(remaining)
            items = remaining
        } catch {
            // The server's result is still authoritative; the entry remains
            // pending locally and prevents switching until cleanup succeeds.
        }
    }

    private func save(_ replies: [PendingReply]) throws {
        try FileManager.default.createDirectory(at: fileURL.deletingLastPathComponent(), withIntermediateDirectories: true)
        try JSONEncoder().encode(replies).write(to: fileURL, options: .atomic)
    }

    private static func storageMessage(_ error: Error) -> String {
        String(localized: "Couldn't save your answer on this iPhone. It has not been sent. Please free up storage or restart Meow and try again. (\(error.localizedDescription))")
    }
}
