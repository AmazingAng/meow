import XCTest
@testable import Meow

private final class PreviewCalls: @unchecked Sendable {
    private let lock = NSLock()
    private var values: [String: Int] = [:]
    private var main = false
    func parse(_ text: String) -> AttributedString? {
        lock.withLock { values[text, default: 0] += 1; main = main || Thread.isMainThread }
        return text == "json" ? nil : AttributedString(text)
    }
    func count(_ text: String) -> Int { lock.withLock { values[text, default: 0] } }
    var ranOnMain: Bool { lock.withLock { main } }
}

final class InboxPreviewCacheTests: XCTestCase {
    func testConcurrentRequestsShareParsingIncludingEmptyResults() async {
        let calls = PreviewCalls(), cache = InboxPreviewCache(parser: { calls.parse($0) })
        for body in ["中文 **测试** 😺", "json"] {
            await withTaskGroup(of: Void.self) { group in
                for _ in 0..<50 { group.addTask {
                    let value = await cache.preview(body)
                    XCTAssertEqual(value.body, body)
                    XCTAssertEqual(value.text, body == "json" ? nil : AttributedString(body))
                } }
            }
            XCTAssertEqual(calls.count(body), 1)
        }
    }

    func testLRUEvictionAndByteBudget() async {
        let calls = PreviewCalls(), cache = InboxPreviewCache(capacity: 2, byteLimit: 10, parser: { calls.parse($0) })
        for body in ["aaaaa", "bbbbb", "aaaaa", "ccccc", "bbbbb"] { _ = await cache.preview(body) }
        XCTAssertEqual(calls.count("aaaaa"), 1)
        XCTAssertEqual(calls.count("bbbbb"), 2)
        XCTAssertEqual(calls.count("ccccc"), 1)
        let limited = InboxPreviewCache(capacity: 128, byteLimit: 8, parser: { calls.parse($0) })
        for body in ["1234", "中文", "1234"] { _ = await limited.preview(body) }
        XCTAssertEqual(calls.count("1234"), 2)
        _ = await limited.preview("中文"); _ = await limited.preview("中文")
        XCTAssertEqual(calls.count("中文"), 2)
        _ = await limited.preview("oversized input"); _ = await limited.preview("oversized input")
        XCTAssertEqual(calls.count("oversized input"), 2, "oversized input must not be retained")
    }

    @MainActor
    func testParsingLeavesMainActorAndCancelledWorkDoesNotPoisonCache() async {
        let calls = PreviewCalls(), cache = InboxPreviewCache(parser: { calls.parse($0) })
        let value = await cache.preview("first")
        XCTAssertEqual(String(value.text!.characters), "first")
        XCTAssertFalse(calls.ranOnMain)
        let task = Task { () -> InboxPreview in
            withUnsafeCurrentTask { $0?.cancel() }
            return await cache.preview("cancelled")
        }
        _ = await task.value
        XCTAssertEqual(calls.count("cancelled"), 0)
        _ = await cache.preview("cancelled")
        XCTAssertEqual(calls.count("cancelled"), 1)
    }
}
