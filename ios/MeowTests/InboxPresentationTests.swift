import XCTest
@testable import Meow

final class InboxPresentationTests: XCTestCase {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    private func item(_ id: String, expiry: TimeInterval? = nil, read: Bool = false,
                      status: QuestionStatus = .pending) -> MeowNotification {
        MeowNotification(id: id, type: expiry == nil ? .info : .question, title: id, body: "", source: "Codex",
                         priority: "normal", pushStatus: "accepted", createdAt: now, readAt: read ? now : nil,
                         question: expiry.map { .init(id: "q_" + id, status: status, expiresAt: now.addingTimeInterval($0)) })
    }

    func testReadQuestionsStillNeedAnAnswerAndSoonestComesFirst() {
        let groups = InboxPresentation([item("info"), item("later", expiry: 600),
                                        item("soon", expiry: 60, read: true)], now: now)
        XCTAssertEqual(groups.pending.map(\.id), ["soon", "later"])
        XCTAssertEqual(groups.activity.map(\.id), ["info"])
    }

    func testExpiredAndResolvedQuestionsMoveToActivityWithoutLosingItems() {
        let input = [item("expired", expiry: 0), item("answered", expiry: 60, status: .answered),
                     item("cancelled", expiry: 60, status: .cancelled), item("live", expiry: 10)]
        let groups = InboxPresentation(input, now: now)
        XCTAssertEqual(groups.pending.map(\.id), ["live"])
        XCTAssertEqual(groups.activity.map(\.id), ["expired", "answered", "cancelled"])
        XCTAssertEqual(Set((groups.pending + groups.activity).map(\.id)), Set(input.map(\.id)))
        XCTAssertTrue(InboxPresentation(input, now: now.addingTimeInterval(10)).pending.isEmpty)
    }
    func testExplicitCategoriesKeepTitleAndSession() {
        var n = item("n")
        n.context = .init(workspace: "meow", session: "修复通知", sessionId: "s1")
        for (title, event) in [("New result", NotificationEvent.result), ("有新结果", .result), ("Needs your attention", .attention), ("需要你确认", .attention), ("Run failed", .failure), ("运行出错", .failure)] {
            n.event = event
            n.title = title + " · meow"
            XCTAssertNotNil(n.inboxCategory)
            XCTAssertEqual(n.compactHeadline, title)
            XCTAssertEqual(n.compactMetadata, "修复通知 · Codex · meow")
        }
        n.event = nil
        n.title = "New result from the benchmark"
        XCTAssertNil(n.inboxCategory)
        XCTAssertEqual(n.compactHeadline, n.title)
        XCTAssertEqual(n.compactMetadata, "修复通知 · Codex · meow")
    }

    func testCompactRowsKeepTitlesWhenSessionIsMissingAndLegacyMessagesIntact() {
        var n = item("n")
        n.title = "Needs your attention · meow"
        XCTAssertNil(n.inboxCategory)
        XCTAssertEqual(n.compactHeadline, n.title)
        n.context = .init(workspace: "meow")
        XCTAssertNil(n.inboxCategory)
        n.event = .attention
        XCTAssertEqual(n.inboxCategory, .attention)
        XCTAssertEqual(n.compactHeadline, "Needs your attention")
        n.context?.sessionId = "session-42"
        XCTAssertEqual(n.compactHeadline, "Needs your attention")
        XCTAssertEqual(n.compactMetadata, "Session · session-42 · Codex · meow")
        n.context?.sessionId = " "
        XCTAssertEqual(n.compactHeadline, "Needs your attention")
    }

}
