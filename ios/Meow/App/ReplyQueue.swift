import Foundation

/// An unchanged record imported from an earlier version, never submitted in V1.
struct PendingReply: Codable, Hashable, Sendable {
    struct Failure: Codable, Hashable, Sendable {
        var status: Int
        var code: String
        var message: String
    }
    var idempotencyKey: String
    var questionID: String
    var answer: String
    var createdAt: Date
    var failure: Failure? = nil
}

/// Read/delete archive of legacy replies. It has no sender or retry operation.
actor ReplyQueue {
    static let shared = ReplyQueue(fileURL: MeowShared.containerURL.appending(path: "pending-replies.json"))
    private let fileURL: URL
    private var items: [PendingReply]
    private var storageError: Error?

    init(fileURL: URL) {
        self.fileURL = fileURL
        do {
            items = try JSONDecoder().decode([PendingReply].self, from: Data(contentsOf: fileURL))
        } catch CocoaError.fileReadNoSuchFile {
            items = []
        } catch {
            items = []
            storageError = error
        }
    }

    var pending: [PendingReply] { items.filter { $0.failure == nil } }
    var failed: [PendingReply] { items.filter { $0.failure != nil } }
    var outstandingCount: Int { items.count }

    /// Both unsent and refused records require explicit discard before reset.
    func pauseIfIdle() -> Bool { items.isEmpty && storageError == nil }

    func discardAndPause() throws {
        try save([])
        items = []
        storageError = nil
    }

    /// Explicit deletion applies to either kind of legacy record.
    func dismissFailure(_ idempotencyKey: String) throws {
        if let storageError { throw storageError }
        let remaining = items.filter { $0.idempotencyKey != idempotencyKey }
        try save(remaining)
        items = remaining
    }

    private func save(_ records: [PendingReply]) throws {
        try FileManager.default.createDirectory(at: fileURL.deletingLastPathComponent(), withIntermediateDirectories: true)
        try JSONEncoder().encode(records).write(to: fileURL, options: .atomic)
    }
}
