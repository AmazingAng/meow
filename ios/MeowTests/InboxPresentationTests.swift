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
}
