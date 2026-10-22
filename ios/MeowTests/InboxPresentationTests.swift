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
    func testRowsLeadWithSessionAndProjectBeforeAgent() {
        var n = item("n")
        n.context = .init(workspace: "meow", session: "修复通知", sessionId: "s1")
        for event in [NotificationEvent.result, .attention, .failure] {
            n.event = event
            n.title = "Custom notification title · meow"
            XCTAssertEqual(n.inboxCategory, event)
            XCTAssertEqual(n.compactHeadline, "修复通知")
            XCTAssertEqual(n.compactMetadata, "meow · Codex")
            XCTAssertEqual(n.title, "Custom notification title · meow")
        }
        // Older notifications can have session context without an event field.
        n.event = nil
        n.title = "New result · meow"
        XCTAssertNil(n.inboxCategory)
        XCTAssertEqual(n.compactHeadline, "修复通知")
        XCTAssertEqual(n.compactMetadata, "meow · Codex")
    }

    func testMissingSessionUsesIDThenNeutralLabelForEvents() {
        var n = item("n")
        n.event = .attention
        n.title = "Needs your attention · meow"
        n.context = .init(workspace: "meow", sessionId: "session-42")
        XCTAssertEqual(n.compactHeadline, String(localized: "Session") + " · session-42")
        XCTAssertEqual(n.compactMetadata, "meow · Codex")
        n.context?.sessionId = " "
        XCTAssertEqual(n.compactHeadline, String(localized: "Session"))
        n.context = nil
        XCTAssertEqual(n.compactHeadline, String(localized: "Session"))
        XCTAssertEqual(n.compactMetadata, "Codex")
    }

    func testOrdinaryServiceWithoutSessionKeepsItsTitle() {
        var n = item("n")
        n.title = "Backup completed"
        n.context = .init(workspace: "backups")
        XCTAssertEqual(n.compactHeadline, "Backup completed")
        XCTAssertEqual(n.compactMetadata, "backups · Codex")
        n.source = "   "
        XCTAssertEqual(n.compactMetadata, "backups · Meow")
        n.context = nil
        XCTAssertEqual(n.compactMetadata, "Meow")
    }
}
