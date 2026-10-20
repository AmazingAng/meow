import XCTest
import UserNotifications
@testable import Meow

@MainActor
final class NotificationOnlyTests: XCTestCase {
    func testAllNotificationCategoriesHaveNoReplyOrApprovalActions() {
        let categories = NotificationCategory.all
        XCTAssertEqual(Set(categories.map(\.identifier)), Set(["MEOW_INFO", "MEOW_REPLY", "MEOW_APPROVAL", "MEOW_CHOICE"]))
        for category in categories {
            XCTAssertTrue(category.actions.isEmpty, "legacy payload \(category.identifier) must not restore an answer button")
        }
    }

    func testInboxOnlyOffersAllAndUnread() {
        XCTAssertEqual(AppModel.Filter.visibleCases, [.all, .unread])
    }

    func testSavedRepliesAreLoadedWithoutSendingAndSurviveRestart() async throws {
        let file = FileManager.default.temporaryDirectory.appending(path: "notification-only-\(UUID()).json")
        defer { try? FileManager.default.removeItem(at: file) }
        let record = PendingReply(idempotencyKey: "old-answer", questionID: "qst_legacy", answer: "My saved answer", createdAt: .now)
        try JSONEncoder().encode([record]).write(to: file)
        let queue = ReplyQueue(fileURL: file) { _ in
            XCTFail("notification-only app must not send a saved reply")
            throw URLError(.notConnectedToInternet)
        }
        let model = AppModel(replies: queue)
        await model.loadSavedReplyRecords()
        XCTAssertEqual(model.queuedReplies[record.questionID]?.answer, record.answer)
        XCTAssertEqual(try JSONDecoder().decode([PendingReply].self, from: Data(contentsOf: file)), [record])
        let restarted = AppModel(replies: queue)
        await restarted.loadSavedReplyRecords()
        XCTAssertEqual(restarted.queuedReplies[record.questionID]?.idempotencyKey, record.idempotencyKey)
    }
}
