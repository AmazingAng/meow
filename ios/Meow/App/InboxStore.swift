import Foundation
import CryptoKit
import SQLite3

/// Only the main app opens this database. All SQLite work runs on one queue;
/// the extension keeps fetching from the server without holding shared DB locks.
final class InboxStore: @unchecked Sendable {
    static let shared = InboxStore(fileURL: defaultURL)
    private static var defaultURL: URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Meow", isDirectory: true).appendingPathComponent("inbox.sqlite")
    }
    private let fileURL: URL
    private let queue = DispatchQueue(label: "blue.meow.inbox-storage", qos: .userInitiated)
    private var db: OpaquePointer?
    private let transient = unsafeBitCast(-1, to: sqlite3_destructor_type.self)

    init(fileURL: URL) { self.fileURL = fileURL }
    deinit { if let db { sqlite3_close_v2(db) } }

    struct Failure: LocalizedError {
        let message: String
        var errorDescription: String? { message }
    }

    static func scope(server: URL, token: String) -> String {
        let normalized = MeowShared.serverURL(from: server.absoluteString)?.absoluteString ?? server.absoluteString
        let bytes = Data((normalized + "\n" + token).utf8)
        return SHA256.hash(data: bytes).map { String(format: "%02x", $0) }.joined()
    }

    func load(scope: String) async throws -> [MeowNotification]? {
        try await perform {
            try self.open()
            let known = try self.prepare("SELECT 1 FROM inbox_scopes WHERE scope=?")
            defer { sqlite3_finalize(known) }
            try self.bind(scope, at: 1, to: known)
            let status = sqlite3_step(known)
            if status == SQLITE_DONE { return nil }
            try self.require(status == SQLITE_ROW)
            let statement = try self.prepare("SELECT payload FROM inbox_messages WHERE scope=? ORDER BY created_at DESC,id DESC")
            defer { sqlite3_finalize(statement) }
            try self.bind(scope, at: 1, to: statement)
            var items: [MeowNotification] = []
            while true {
                let status = sqlite3_step(statement)
                if status == SQLITE_DONE { break }
                try self.require(status == SQLITE_ROW)
                guard let bytes = sqlite3_column_blob(statement, 0) else {
                    throw Failure(message: "A saved message is unreadable.")
                }
                let data = Data(bytes: bytes, count: Int(sqlite3_column_bytes(statement, 0)))
                items.append(try JSONDecoder().decode(MeowNotification.self, from: data))
            }
            return items
        }
    }

    func save(_ items: [MeowNotification], scope: String) async throws {
        try await perform { try self.write(items, scope: scope) }
    }

    func erase(scope: String) async throws {
        try await perform {
            try self.open()
            try self.transaction {
                for table in ["inbox_messages", "inbox_scopes"] {
                    let statement = try self.prepare("DELETE FROM \(table) WHERE scope=?")
                    defer { sqlite3_finalize(statement) }
                    try self.bind(scope, at: 1, to: statement)
                    try self.require(sqlite3_step(statement) == SQLITE_DONE)
                }
            }
        }
    }

    /// Called synchronously on the model actor so successive snapshots enter the
    /// serial queue in order, including optimistic reads and their rollbacks.
    func enqueueSave(_ items: [MeowNotification], scope: String,
                     completion: @escaping @Sendable (Result<Void, Error>) -> Void) {
        queue.async {
            completion(Result { try self.write(items, scope: scope) })
        }
    }

    private func perform<T>(_ work: @escaping () throws -> T) async throws -> T {
        try await withCheckedThrowingContinuation { continuation in
            queue.async { continuation.resume(with: Result { try work() }) }
        }
    }

    private func open() throws {
        if db != nil { return }
        var folder = fileURL.deletingLastPathComponent()
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        var values = URLResourceValues(); values.isExcludedFromBackup = true
        try folder.setResourceValues(values)
        #if os(iOS)
        try FileManager.default.setAttributes([.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication],
                                              ofItemAtPath: folder.path)
        #endif
        var handle: OpaquePointer?
        let status = sqlite3_open_v2(fileURL.path, &handle, SQLITE_OPEN_CREATE | SQLITE_OPEN_READWRITE | SQLITE_OPEN_FULLMUTEX, nil)
        guard status == SQLITE_OK, let handle else {
            if let handle { sqlite3_close_v2(handle) }
            throw Failure(message: "Couldn't open the saved-message database (SQLite \(status)).")
        }
        db = handle
        do {
            try require(sqlite3_busy_timeout(handle, 3000) == SQLITE_OK)
            try execute("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;")
            try execute("CREATE TABLE IF NOT EXISTS inbox_scopes(scope TEXT PRIMARY KEY);")
            try execute("CREATE TABLE IF NOT EXISTS inbox_messages(scope TEXT NOT NULL REFERENCES inbox_scopes(scope),id TEXT NOT NULL,created_at REAL NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(scope,id));")
        } catch {
            sqlite3_close_v2(handle); db = nil; throw error
        }
    }

    private func write(_ items: [MeowNotification], scope: String) throws {
        let encoder = JSONEncoder(); encoder.outputFormatting = [.sortedKeys]
        guard Set(items.map(\.id)).count == items.count else { throw Failure(message: "Duplicate saved-message IDs.") }
        let encoded = try items.map { try encoder.encode($0) }
        try open()
        try transaction {
            try execute("CREATE TEMP TABLE IF NOT EXISTS incoming_ids(id TEXT PRIMARY KEY); DELETE FROM incoming_ids;")
            let scopeStatement = try prepare("INSERT OR IGNORE INTO inbox_scopes(scope) VALUES(?)")
            defer { sqlite3_finalize(scopeStatement) }
            try bind(scope, at: 1, to: scopeStatement)
            try require(sqlite3_step(scopeStatement) == SQLITE_DONE)
            let insert = try prepare("INSERT INTO inbox_messages(scope,id,created_at,payload) VALUES(?,?,?,?) ON CONFLICT(scope,id) DO UPDATE SET created_at=excluded.created_at,payload=excluded.payload WHERE inbox_messages.payload!=excluded.payload")
            let seen = try prepare("INSERT INTO incoming_ids(id) VALUES(?)")
            defer { sqlite3_finalize(insert); sqlite3_finalize(seen) }
            for (item, data) in zip(items, encoded) {
                sqlite3_reset(insert); sqlite3_clear_bindings(insert)
                try bind(scope, at: 1, to: insert); try bind(item.id, at: 2, to: insert)
                try require(sqlite3_bind_double(insert, 3, item.createdAt.timeIntervalSince1970) == SQLITE_OK)
                let code = data.withUnsafeBytes { sqlite3_bind_blob(insert, 4, $0.baseAddress, Int32($0.count), transient) }
                try require(code == SQLITE_OK)
                try require(sqlite3_step(insert) == SQLITE_DONE)
                sqlite3_reset(seen); sqlite3_clear_bindings(seen)
                try bind(item.id, at: 1, to: seen)
                try require(sqlite3_step(seen) == SQLITE_DONE)
            }
            let prune = try prepare("DELETE FROM inbox_messages WHERE scope=? AND id NOT IN (SELECT id FROM incoming_ids)")
            defer { sqlite3_finalize(prune) }
            try bind(scope, at: 1, to: prune)
            try require(sqlite3_step(prune) == SQLITE_DONE)
        }
    }

    private func transaction(_ work: () throws -> Void) throws {
        try execute("BEGIN IMMEDIATE")
        do { try work(); try execute("COMMIT") }
        catch {
            let original = error
            do { try execute("ROLLBACK") }
            catch { throw Failure(message: "Couldn't roll back saved messages: \(error.localizedDescription)") }
            throw original
        }
    }

    private func prepare(_ sql: String) throws -> OpaquePointer {
        var statement: OpaquePointer?
        let status = sqlite3_prepare_v2(db, sql, -1, &statement, nil)
        try require(status == SQLITE_OK)
        guard let statement else { throw Failure(message: "Couldn't prepare the saved-message query.") }
        return statement
    }
    private func execute(_ sql: String) throws { try require(sqlite3_exec(db, sql, nil, nil, nil) == SQLITE_OK) }
    private func bind(_ value: String, at index: Int32, to statement: OpaquePointer) throws {
        try require(sqlite3_bind_text(statement, index, value, -1, transient) == SQLITE_OK)
    }
    private func require(_ success: Bool) throws {
        guard success else {
            throw Failure(message: db.map { String(cString: sqlite3_errmsg($0)) } ?? "Saved-message database is unavailable.")
        }
    }
}
