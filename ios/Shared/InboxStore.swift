import CryptoKit
import Foundation
import SQLite3

/// App and notification extension share this WAL database. Each connection
/// serializes its own work; SQLite transactions coordinate the two processes.
final class InboxStore: @unchecked Sendable {
    static let shared = InboxStore(fileURL: defaultURL, legacyURL: legacyPrivateURL)
    static let notificationStore = InboxStore(fileURL: defaultURL, busyTimeout: 100)
    private static var defaultURL: URL {
        MeowShared.containerURL.appendingPathComponent("Inbox", isDirectory: true).appendingPathComponent(
            "inbox.sqlite")
    }
    private static var legacyPrivateURL: URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Meow", isDirectory: true).appendingPathComponent("inbox.sqlite")
    }
    private let fileURL: URL
    private let queue = DispatchQueue(label: "blue.meow.inbox-storage", qos: .userInitiated)
    private var db: OpaquePointer?
    private let transient = unsafeBitCast(-1, to: sqlite3_destructor_type.self)

    private let legacyURL: URL?
    private let busyTimeout: Int32
    init(fileURL: URL, legacyURL: URL? = nil, busyTimeout: Int32 = 3000) {
        self.fileURL = fileURL
        self.legacyURL = legacyURL
        self.busyTimeout = busyTimeout
    }

    /// Only the main app calls this; the extension cannot see its old sandbox.
    func prepareForApp() async throws {
        guard let legacyURL else { return }
        try await perform {
            try self.open()
            guard try self.metadata("legacy_imported") == nil else { return }
            var source: OpaquePointer?
            if FileManager.default.fileExists(atPath: legacyURL.path) {
                guard
                    sqlite3_open_v2(legacyURL.path, &source, SQLITE_OPEN_READONLY | SQLITE_OPEN_FULLMUTEX, nil)
                        == SQLITE_OK,
                    source != nil
                else {
                    if let source { sqlite3_close_v2(source) }
                    throw Failure(message: "Couldn't open the previous message history.")
                }
            }
            defer { if let source { sqlite3_close_v2(source) } }
            try self.transaction {
                if let source {
                    var scopes: OpaquePointer?
                    var rows: OpaquePointer?
                    defer {
                        sqlite3_finalize(scopes)
                        sqlite3_finalize(rows)
                    }
                    guard sqlite3_prepare_v2(source, "SELECT scope FROM inbox_scopes", -1, &scopes, nil) == SQLITE_OK,
                        sqlite3_prepare_v2(source, "SELECT scope,payload FROM inbox_messages", -1, &rows, nil)
                            == SQLITE_OK
                    else { throw Failure(message: "Couldn't read the previous message history.") }
                    while true {
                        let status = sqlite3_step(scopes)
                        if status == SQLITE_DONE { break }
                        guard status == SQLITE_ROW, let raw = sqlite3_column_text(scopes, 0) else {
                            throw Failure(message: "Invalid previous history scope.")
                        }
                        try self.ensureScope(String(cString: raw))
                    }
                    let decoder = JSONDecoder()
                    while true {
                        let status = sqlite3_step(rows)
                        if status == SQLITE_DONE { break }
                        guard status == SQLITE_ROW, let raw = sqlite3_column_text(rows, 0),
                            let bytes = sqlite3_column_blob(rows, 1)
                        else { throw Failure(message: "Invalid previous message history.") }
                        let data = Data(bytes: bytes, count: Int(sqlite3_column_bytes(rows, 1)))
                        let item = try decoder.decode(MeowNotification.self, from: data)
                        try self.writeRows([item], encoded: [data], scope: String(cString: raw), importing: true)
                    }
                }
                try self.setMetadata("legacy_imported", value: "1")
            }
        }
    }

    func activate(scope: String) async throws {
        try await perform {
            try self.open()
            try self.transaction {
                try self.ensureScope(scope)
                try self.setMetadata("active_scope", value: scope)
            }
        }
    }

    func receiveVerified(
        _ item: MeowNotification, scope: String,
        isCurrent: @escaping @Sendable () -> Bool = { true }
    ) async throws {
        var incoming = item
        incoming.readAt = nil
        try await perform {
            try self.open()
            try self.transaction {
                guard isCurrent(), try self.metadata("active_scope") == scope,
                    !(try self.isDeleted(item.id, scope: scope))
                else { throw Failure(message: "The notification no longer belongs to this inbox.") }
                let encoder = JSONEncoder()
                encoder.outputFormatting = [.sortedKeys]
                try self.writeRows([incoming], encoded: [try encoder.encode(incoming)], scope: scope)
                guard isCurrent() else { throw Failure(message: "The notification registration changed.") }
            }
        }
    }

    func markLocalRead(_ ids: Set<String>, scope: String, at: Date = .now, pendingItems: [MeowNotification] = [])
        async throws
    {
        guard Set(pendingItems.map(\.id)).isSubset(of: ids) else {
            throw Failure(message: "Pending messages must belong to the read selection.")
        }
        try await perform {
            try self.open()
            let encoder = JSONEncoder()
            encoder.outputFormatting = [.sortedKeys]
            let pendingEncoded = try pendingItems.map { try encoder.encode($0) }
            try self.transaction {
                guard try self.metadata("active_scope") == scope else {
                    throw Failure(message: "The inbox registration changed.")
                }
                try self.writeRows(pendingItems, encoded: pendingEncoded, scope: scope)
                let rows = Array(ids)
                for start in stride(from: 0, to: rows.count, by: 200) {
                    let batch = Array(rows[start..<min(start + 200, rows.count)])
                    let marks = Array(repeating: "?", count: batch.count).joined(separator: ",")
                    let statement = try self.prepare(
                        "UPDATE inbox_messages SET read_at=? WHERE scope=? AND read_at IS NULL AND id IN (\(marks))")
                    defer { sqlite3_finalize(statement) }
                    try self.require(sqlite3_bind_double(statement, 1, at.timeIntervalSinceReferenceDate) == SQLITE_OK)
                    try self.bind(scope, at: 2, to: statement)
                    for (i, id) in batch.enumerated() { try self.bind(id, at: Int32(i + 3), to: statement) }
                    try self.require(sqlite3_step(statement) == SQLITE_DONE)
                }
            }
        }
    }

    func unreadCount(scope: String) async throws -> Int {
        try await perform {
            try self.open()
            let statement = try self.prepare("SELECT count(*) FROM inbox_messages WHERE scope=? AND read_at IS NULL")
            defer { sqlite3_finalize(statement) }
            try self.bind(scope, at: 1, to: statement)
            try self.require(sqlite3_step(statement) == SQLITE_ROW)
            return Int(sqlite3_column_int64(statement, 0))
        }
    }
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
            let statement = try self.prepare(
                "SELECT payload,read_at FROM inbox_messages WHERE scope=? ORDER BY created_at DESC,id DESC")
            defer { sqlite3_finalize(statement) }
            try self.bind(scope, at: 1, to: statement)
            var items: [MeowNotification] = []
            let decoder = JSONDecoder()
            while true {
                let status = sqlite3_step(statement)
                if status == SQLITE_DONE { break }
                try self.require(status == SQLITE_ROW)
                guard let bytes = sqlite3_column_blob(statement, 0) else {
                    throw Failure(message: "A saved message is unreadable.")
                }
                let data = Data(bytes: bytes, count: Int(sqlite3_column_bytes(statement, 0)))
                var item = try decoder.decode(MeowNotification.self, from: data)
                item.readAt =
                    sqlite3_column_type(statement, 1) == SQLITE_NULL
                    ? nil : Date(timeIntervalSinceReferenceDate: sqlite3_column_double(statement, 1))
                items.append(item)
            }
            return items
        }
    }

    func save(_ items: [MeowNotification], scope: String) async throws {
        try await perform { try self.write(items, scope: scope) }
    }

    func applyChanges(_ items: [MeowNotification], deleting: Set<String> = [], scope: String) async throws {
        try await perform { try self.writeChanges(items, deleting: deleting, scope: scope) }
    }

    /// Enqueue a small immutable patch in model order. Unmentioned history is
    /// never encoded, scanned for pruning or removed by this operation.
    func enqueueChanges(
        _ items: [MeowNotification], deleting: Set<String>, scope: String,
        completion: @escaping @Sendable (Result<Void, Error>) -> Void
    ) {
        queue.async { completion(Result { try self.writeChanges(items, deleting: deleting, scope: scope) }) }
    }

    func erase(scope: String) async throws {
        try await perform {
            try self.open()
            try self.transaction {
                if try self.metadata("active_scope") == scope { try self.setMetadata("active_scope", value: nil) }
                for table in ["inbox_deletions", "inbox_messages", "inbox_scopes"] {
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
    func enqueueSave(
        _ items: [MeowNotification], scope: String,
        completion: @escaping @Sendable (Result<Void, Error>) -> Void
    ) {
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
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try folder.setResourceValues(values)
        #if os(iOS)
            try FileManager.default.setAttributes(
                [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication],
                ofItemAtPath: folder.path)
        #endif
        var handle: OpaquePointer?
        let status = sqlite3_open_v2(
            fileURL.path, &handle, SQLITE_OPEN_CREATE | SQLITE_OPEN_READWRITE | SQLITE_OPEN_FULLMUTEX, nil)
        guard status == SQLITE_OK, let handle else {
            if let handle { sqlite3_close_v2(handle) }
            throw Failure(message: "Couldn't open the saved-message database (SQLite \(status)).")
        }
        db = handle
        do {
            try require(sqlite3_busy_timeout(handle, busyTimeout) == SQLITE_OK)
            try execute("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;")
            try execute("CREATE TABLE IF NOT EXISTS inbox_scopes(scope TEXT PRIMARY KEY);")
            try execute(
                "CREATE TABLE IF NOT EXISTS inbox_messages(scope TEXT NOT NULL REFERENCES inbox_scopes(scope),id TEXT NOT NULL,created_at REAL NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(scope,id));"
            )
            try transaction {
                let columns = try prepare("PRAGMA table_info(inbox_messages)")
                defer { sqlite3_finalize(columns) }
                var hasRead = false
                while sqlite3_step(columns) == SQLITE_ROW {
                    if let raw = sqlite3_column_text(columns, 1), String(cString: raw) == "read_at" { hasRead = true }
                }
                if !hasRead {
                    try execute("ALTER TABLE inbox_messages ADD COLUMN read_at REAL")
                    let rows = try prepare("SELECT scope,id,payload FROM inbox_messages")
                    let update = try prepare("UPDATE inbox_messages SET read_at=? WHERE scope=? AND id=?")
                    defer {
                        sqlite3_finalize(rows)
                        sqlite3_finalize(update)
                    }
                    let decoder = JSONDecoder()
                    while true {
                        let status = sqlite3_step(rows)
                        if status == SQLITE_DONE { break }
                        try require(status == SQLITE_ROW)
                        guard let bytes = sqlite3_column_blob(rows, 2), let scope = sqlite3_column_text(rows, 0),
                            let id = sqlite3_column_text(rows, 1)
                        else { throw Failure(message: "Invalid saved message.") }
                        let item = try decoder.decode(
                            MeowNotification.self, from: Data(bytes: bytes, count: Int(sqlite3_column_bytes(rows, 2))))
                        if let date = item.readAt {
                            sqlite3_reset(update)
                            sqlite3_clear_bindings(update)
                            try require(
                                sqlite3_bind_double(update, 1, date.timeIntervalSinceReferenceDate) == SQLITE_OK)
                            try bind(String(cString: scope), at: 2, to: update)
                            try bind(String(cString: id), at: 3, to: update)
                            try require(sqlite3_step(update) == SQLITE_DONE)
                        }
                    }
                }
                try execute("CREATE TABLE IF NOT EXISTS inbox_meta(key TEXT PRIMARY KEY,value TEXT NOT NULL)")
                try execute(
                    "CREATE TABLE IF NOT EXISTS inbox_deletions(scope TEXT NOT NULL,id TEXT NOT NULL,PRIMARY KEY(scope,id))"
                )
                try execute("CREATE INDEX IF NOT EXISTS inbox_messages_unread ON inbox_messages(scope,read_at)")
            }
            try execute(
                "CREATE INDEX IF NOT EXISTS inbox_messages_order ON inbox_messages(scope,created_at DESC,id DESC);")
        } catch {
            sqlite3_close_v2(handle)
            db = nil
            throw error
        }
    }

    private func writeChanges(_ items: [MeowNotification], deleting: Set<String>, scope: String) throws {
        let ids = Set(items.map(\.id))
        guard ids.count == items.count, ids.isDisjoint(with: deleting) else {
            throw Failure(message: "Conflicting saved-message changes.")
        }
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        let encoded = try items.map { try encoder.encode($0) }
        try open()
        try transaction {
            let known = try prepare("INSERT OR IGNORE INTO inbox_scopes(scope) VALUES(?)")
            defer { sqlite3_finalize(known) }
            try bind(scope, at: 1, to: known)
            try require(sqlite3_step(known) == SQLITE_DONE)
            try writeRows(items, encoded: encoded, scope: scope)
            let tombstone = try prepare("INSERT OR IGNORE INTO inbox_deletions(scope,id) VALUES(?,?)")
            defer { sqlite3_finalize(tombstone) }
            for id in deleting {
                sqlite3_reset(tombstone)
                sqlite3_clear_bindings(tombstone)
                try bind(scope, at: 1, to: tombstone)
                try bind(id, at: 2, to: tombstone)
                try require(sqlite3_step(tombstone) == SQLITE_DONE)
            }
            let remove = try prepare("DELETE FROM inbox_messages WHERE scope=? AND id=?")
            defer { sqlite3_finalize(remove) }
            for id in deleting {
                sqlite3_reset(remove)
                sqlite3_clear_bindings(remove)
                try bind(scope, at: 1, to: remove)
                try bind(id, at: 2, to: remove)
                try require(sqlite3_step(remove) == SQLITE_DONE)
            }
        }
    }

    private func write(_ items: [MeowNotification], scope: String) throws {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        guard Set(items.map(\.id)).count == items.count else { throw Failure(message: "Duplicate saved-message IDs.") }
        let encoded = try items.map { try encoder.encode($0) }
        try open()
        try transaction {
            try execute("CREATE TEMP TABLE IF NOT EXISTS incoming_ids(id TEXT PRIMARY KEY); DELETE FROM incoming_ids;")
            let scopeStatement = try prepare("INSERT OR IGNORE INTO inbox_scopes(scope) VALUES(?)")
            defer { sqlite3_finalize(scopeStatement) }
            try bind(scope, at: 1, to: scopeStatement)
            try require(sqlite3_step(scopeStatement) == SQLITE_DONE)
            let seen = try prepare("INSERT INTO incoming_ids(id) VALUES(?)")
            defer { sqlite3_finalize(seen) }
            try writeRows(items, encoded: encoded, scope: scope)
            for item in items {
                sqlite3_reset(seen)
                sqlite3_clear_bindings(seen)
                try bind(item.id, at: 1, to: seen)
                try require(sqlite3_step(seen) == SQLITE_DONE)
            }
            let prune = try prepare(
                "DELETE FROM inbox_messages WHERE scope=? AND id NOT IN (SELECT id FROM incoming_ids)")
            defer { sqlite3_finalize(prune) }
            try bind(scope, at: 1, to: prune)
            try require(sqlite3_step(prune) == SQLITE_DONE)
        }
    }

    private func ensureScope(_ scope: String) throws {
        let statement = try prepare("INSERT OR IGNORE INTO inbox_scopes(scope) VALUES(?)")
        defer { sqlite3_finalize(statement) }
        try bind(scope, at: 1, to: statement)
        try require(sqlite3_step(statement) == SQLITE_DONE)
    }
    private func metadata(_ key: String) throws -> String? {
        let statement = try prepare("SELECT value FROM inbox_meta WHERE key=?")
        defer { sqlite3_finalize(statement) }
        try bind(key, at: 1, to: statement)
        let status = sqlite3_step(statement)
        if status == SQLITE_DONE { return nil }
        try require(status == SQLITE_ROW)
        return String(cString: sqlite3_column_text(statement, 0))
    }
    private func setMetadata(_ key: String, value: String?) throws {
        let statement = try prepare(
            value == nil
                ? "DELETE FROM inbox_meta WHERE key=?"
                : "INSERT INTO inbox_meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value")
        defer { sqlite3_finalize(statement) }
        try bind(key, at: 1, to: statement)
        if let value { try bind(value, at: 2, to: statement) }
        try require(sqlite3_step(statement) == SQLITE_DONE)
    }
    private func isDeleted(_ id: String, scope: String) throws -> Bool {
        let statement = try prepare("SELECT 1 FROM inbox_deletions WHERE scope=? AND id=?")
        defer { sqlite3_finalize(statement) }
        try bind(scope, at: 1, to: statement)
        try bind(id, at: 2, to: statement)
        let status = sqlite3_step(statement)
        try require(status == SQLITE_ROW || status == SQLITE_DONE)
        return status == SQLITE_ROW
    }
    private func writeRows(_ items: [MeowNotification], encoded: [Data], scope: String, importing: Bool = false) throws
    {
        let payload = importing ? "inbox_messages.payload" : "excluded.payload"
        let insert = try prepare(
            "INSERT INTO inbox_messages(scope,id,created_at,payload,read_at) SELECT ?,?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM inbox_deletions WHERE scope=? AND id=?) ON CONFLICT(scope,id) DO UPDATE SET created_at=excluded.created_at,payload=\(payload),read_at=COALESCE(inbox_messages.read_at,excluded.read_at) WHERE inbox_messages.payload!=excluded.payload OR (inbox_messages.read_at IS NULL AND excluded.read_at IS NOT NULL)"
        )
        defer { sqlite3_finalize(insert) }
        for (item, data) in zip(items, encoded) {
            sqlite3_reset(insert)
            sqlite3_clear_bindings(insert)
            try bind(scope, at: 1, to: insert)
            try bind(item.id, at: 2, to: insert)
            try require(sqlite3_bind_double(insert, 3, item.createdAt.timeIntervalSince1970) == SQLITE_OK)
            try require(
                data.withUnsafeBytes { sqlite3_bind_blob(insert, 4, $0.baseAddress, Int32($0.count), transient) }
                    == SQLITE_OK)
            if let date = item.readAt {
                try require(sqlite3_bind_double(insert, 5, date.timeIntervalSinceReferenceDate) == SQLITE_OK)
            }
            try bind(scope, at: 6, to: insert)
            try bind(item.id, at: 7, to: insert)
            try require(sqlite3_step(insert) == SQLITE_DONE)
        }
    }

    private func transaction(_ work: () throws -> Void) throws {
        try execute("BEGIN IMMEDIATE")
        do {
            try work()
            try execute("COMMIT")
        } catch {
            let original = error
            do { try execute("ROLLBACK") } catch {
                throw Failure(message: "Couldn't roll back saved messages: \(error.localizedDescription)")
            }
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
            throw Failure(
                message: db.map { String(cString: sqlite3_errmsg($0)) } ?? "Saved-message database is unavailable.")
        }
    }
}
