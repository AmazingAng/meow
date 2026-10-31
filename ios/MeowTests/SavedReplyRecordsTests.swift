import XCTest
@testable import Meow

final class SavedReplyRecordsTests: XCTestCase {
    private var file: URL { FileManager.default.temporaryDirectory.appending(path: "saved-records-\(UUID()).json") }

    func testLegacyRecordsSurviveLoadingAndRestartWithoutChanges() async throws {
        let file = file
        defer { try? FileManager.default.removeItem(at: file) }
        for count in [1, 8, 64] {
            let records = (0..<count).map { index in
                PendingReply(idempotencyKey: "original-\(index)", questionID: "question-\(index)",
                             answer: String(repeating: "猫😺\n\t", count: index + 1), createdAt: Date(timeIntervalSince1970: Double(index)),
                             failure: index % 2 == 0 ? nil : .init(status: 410, code: "expired", message: "Original reason"))
            }
            let original = try JSONEncoder().encode(records)
            try original.write(to: file)
            for _ in 0..<2 {
                let archive = ReplyQueue(fileURL: file)
                let pending = await archive.pending
                let failed = await archive.failed
                XCTAssertEqual(Set(pending + failed), Set(records))
                XCTAssertEqual(try Data(contentsOf: file), original)
            }
        }
    }

    func testPendingAndFailedRecordsBothRequireExplicitDiscard() async throws {
        let file = file
        defer { try? FileManager.default.removeItem(at: file) }
        let failures: [PendingReply.Failure?] = [nil, .init(status: 404, code: "not_found", message: "Gone")]
        for failure in failures {
            let record = PendingReply(idempotencyKey: "original", questionID: "question", answer: "keep", createdAt: .now, failure: failure)
            try JSONEncoder().encode([record]).write(to: file)
            let archive = ReplyQueue(fileURL: file)
            let allowed = await archive.pauseIfIdle()
            XCTAssertFalse(allowed)
            let count = await archive.outstandingCount
            XCTAssertEqual(count, 1)
            XCTAssertEqual(try JSONDecoder().decode([PendingReply].self, from: Data(contentsOf: file)), [record])
            try await archive.discardAndPause()
            let empty = await archive.outstandingCount
            XCTAssertEqual(empty, 0)
        }
    }

    func testEitherRecordCanBeExplicitlyDeletedAndWriteErrorsPreserveText() async throws {
        let root = file
        let backup = root.appendingPathExtension("backup")
        defer { try? FileManager.default.removeItem(at: root); try? FileManager.default.removeItem(at: backup) }
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        let path = root.appending(path: "records.json")
        let record = PendingReply(idempotencyKey: "original", questionID: "q", answer: "keep original", createdAt: .now)
        try JSONEncoder().encode([record]).write(to: path)
        let archive = ReplyQueue(fileURL: path)
        try FileManager.default.moveItem(at: root, to: backup)
        try Data().write(to: root)
        do { try await archive.dismissFailure(record.idempotencyKey); XCTFail("failed write must be surfaced") } catch {}
        let kept = await archive.pending
        XCTAssertEqual(kept, [record])
        try FileManager.default.removeItem(at: root)
        try FileManager.default.moveItem(at: backup, to: root)
        try await archive.dismissFailure(record.idempotencyKey)
        XCTAssertTrue(try JSONDecoder().decode([PendingReply].self, from: Data(contentsOf: path)).isEmpty)
    }

    func testCorruptArchiveIsNotSilentlyOverwritten() async throws {
        let file = file
        defer { try? FileManager.default.removeItem(at: file) }
        let original = Data("corrupt original".utf8)
        try original.write(to: file)
        let archive = ReplyQueue(fileURL: file)
        let allowed = await archive.pauseIfIdle()
        XCTAssertFalse(allowed)
        do { try await archive.dismissFailure("missing"); XCTFail("must not rewrite corrupt history") } catch {}
        XCTAssertEqual(try Data(contentsOf: file), original)
        try await archive.discardAndPause()
        XCTAssertTrue(try JSONDecoder().decode([PendingReply].self, from: Data(contentsOf: file)).isEmpty)
    }
}
