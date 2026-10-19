import XCTest
import UserNotifications
@testable import Meow

final class NotificationCallbackTests: XCTestCase {
    func testBackgroundResponseEntryCompletesOnceOnMainThreadAfterSuspension() async {
        let done = expectation(description: "system response completion")
        done.assertForOverFulfill = true
        DispatchQueue.global(qos: .userInitiated).async {
            XCTAssertFalse(Thread.isMainThread)
            NotificationCallback.run {
                XCTAssertTrue(Thread.isMainThread, "response handling uses MainActor")
                try? await Task.sleep(for: .milliseconds(10))
                XCTAssertTrue(Thread.isMainThread, "handling resumes on MainActor")
                return "handled"
            } completion: { result in
                XCTAssertTrue(Thread.isMainThread, "UIKit restoration completion must not run on a cooperative worker")
                XCTAssertEqual(result, "handled")
                done.fulfill()
            }
        }
        await fulfillment(of: [done], timeout: 3)
    }

    func testRejectedOrEmptyResponseStillCompletesOnMainThread() async {
        let done = expectation(description: "ignored action completion")
        done.assertForOverFulfill = true
        DispatchQueue.global().async {
            NotificationCallback.run {
                // Includes stale bindings, missing metadata, and unsupported
                // action identifiers: no work, but the OS must be released.
                return
            } completion: { _ in
                XCTAssertTrue(Thread.isMainThread)
                done.fulfill()
            }
        }
        await fulfillment(of: [done], timeout: 3)
    }

    func testForegroundPresentationReturnsOptionsOnMainThread() async {
        let done = expectation(description: "presentation completion")
        done.assertForOverFulfill = true
        let expected: UNNotificationPresentationOptions = [.banner, .list, .sound, .badge]
        DispatchQueue.global().async {
            NotificationCallback.run { expected } completion: { options in
                XCTAssertTrue(Thread.isMainThread)
                XCTAssertEqual(options, expected)
                done.fulfill()
            }
        }
        await fulfillment(of: [done], timeout: 3)
    }
}
