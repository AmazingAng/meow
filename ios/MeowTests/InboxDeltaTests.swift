import SQLite3
import XCTest

@testable import Meow

final class InboxDeltaTests: XCTestCase {
    private func item(_ id: String, body: String = "original") -> MeowNotification {
        MeowNotification(
            id: id, type: .info, title: id, body: body, source: "fixture", priority: "normal", pushStatus: "accepted",
            createdAt: .now)
    }

    func testDeltaPreservesUnmentionedHistoryAndOtherScope() async throws {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
        try await store.save([item("one"), item("two")], scope: "a")
        try await store.save([item("one", body: "other-device")], scope: "b")
        try await store.applyChanges([item("one", body: "updated")], scope: "a")
        let a = try await store.load(scope: "a")
        let b = try await store.load(scope: "b")
        XCTAssertEqual(a?.count, 2)
        XCTAssertEqual(a?.first(where: { $0.id == "one" })?.body, "updated")
        XCTAssertEqual(b?.first?.body, "other-device")
        try await store.applyChanges([], deleting: ["one"], scope: "a")
        let remaining = try await store.load(scope: "a")
        XCTAssertEqual(remaining?.map(\.id), ["two"])
    }

    func testDeltaRollbackAndEmptyScope() async throws {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let file = folder.appendingPathComponent("inbox.sqlite")
        let store = InboxStore(fileURL: file)
        try await store.save([item("one"), item("two")], scope: "scope")
        var db: OpaquePointer?
        XCTAssertEqual(sqlite3_open(file.path, &db), SQLITE_OK)
        defer { sqlite3_close(db) }
        XCTAssertEqual(
            sqlite3_exec(
                db,
                "CREATE TRIGGER fail_delta BEFORE DELETE ON inbox_messages BEGIN SELECT RAISE(ABORT,'injected'); END",
                nil, nil, nil), SQLITE_OK)
        do {
            try await store.applyChanges([item("one", body: "must roll back")], deleting: ["two"], scope: "scope")
            XCTFail("delta failure swallowed")
        } catch {}
        let restored = try await store.load(scope: "scope")
        XCTAssertEqual(restored?.count, 2)
        XCTAssertEqual(restored?.first(where: { $0.id == "one" })?.body, "original")
        try await store.applyChanges([], scope: "empty")
        let empty = try await store.load(scope: "empty")
        XCTAssertEqual(empty, [])
    }
    func testGeneratedPatchesAreAtomicIsolatedAndIdempotent() async throws {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
        var expected: [String: MeowNotification] = [:]
        var tombstones: Set<String> = []
        try await store.save([item("foreign")], scope: "other")
        for round in 0..<60 {
            let id = "消息-\(round % 17)"
            let value = item(id, body: "round \(round) 😺")
            let deleted: Set<String> = round % 3 == 0 ? ["消息-\((round + 1) % 17)"] : []
            if !tombstones.contains(id) { expected[id] = value }
            for id in deleted { expected[id] = nil }; tombstones.formUnion(deleted)
            try await store.applyChanges([value], deleting: deleted, scope: "local")
            try await store.applyChanges([value], deleting: deleted, scope: "local")
            let actual = try await store.load(scope: "local")
            XCTAssertEqual(Dictionary(uniqueKeysWithValues: (actual ?? []).map { ($0.id, $0) }), expected)
        }
        do {
            try await store.applyChanges([item("duplicate"), item("duplicate")], scope: "local")
            XCTFail("duplicate accepted")
        } catch {}
        do {
            try await store.applyChanges([item("conflict")], deleting: ["conflict"], scope: "local")
            XCTFail("conflicting patch accepted")
        } catch {}
        let foreign = try await store.load(scope: "other")
        XCTAssertEqual(foreign?.map(\.id), ["foreign"])
        try await store.erase(scope: "local")
        let erased = try await store.load(scope: "local")
        XCTAssertNil(erased)
    }

    func testReleaseDeltaAvoidsReencodingTenThousandBodies() async throws {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
        var items = (0..<10000).map { item(String($0), body: String(repeating: "正文", count: 512)) }
        try await store.save(items, scope: "benchmark")
        items[0].readAt = .now
        let fullStart = Date()
        try await store.save(items, scope: "benchmark")
        let full = Date().timeIntervalSince(fullStart)
        items[1].readAt = .now
        let deltaStart = Date()
        try await store.applyChanges([items[1]], scope: "benchmark")
        let delta = Date().timeIntervalSince(deltaStart)
        print("PERFORMANCE storage full_ms=\(full * 1000) delta_ms=\(delta * 1000) records=10000")
        XCTAssertLessThan(delta, full)
        let restored = try await store.load(scope: "benchmark")
        XCTAssertEqual(restored?.count, 10000)
        XCTAssertEqual(restored?.first(where: { $0.id == "1" })?.readAt, items[1].readAt)
    }

}
