import UserNotifications
import XCTest

@testable import Meow

private final class AuxiliaryGate: @unchecked Sendable {
    private let lock = NSLock()
    private var opened = false
    private var waiting: [CheckedContinuation<Void, Never>] = []
    func wait() async {
        await withCheckedContinuation { continuation in
            let release = lock.withLock {
                if opened { return true }
                waiting.append(continuation)
                return false
            }
            if release { continuation.resume() }
        }
    }
    func release() {
        let old = lock.withLock {
            opened = true
            defer { waiting = [] }
            return waiting
        }
        for continuation in old { continuation.resume() }
    }
}

private final class LatencyCapture: @unchecked Sendable {
    private let lock = NSLock()
    private var stored: [UNNotificationContent] = []
    func append(_ c: UNNotificationContent) { lock.withLock { stored.append(c) } }
    var values: [UNNotificationContent] { lock.withLock { stored } }
}

final class NotificationLatencyTests: XCTestCase {
    private let identity = NotificationIdentity(
        server: URL(string: "https://latency.invalid")!, token: "mdt_disposable_latency", deviceID: "dev_latency",
        bindingID: "bind_latency")
    private func wake() -> UNNotificationContent {
        let c = UNMutableNotificationContent()
        c.userInfo = ["meow": ["id": "ntf_latency", "relay": true, "binding_id": "bind_latency"]]
        return c
    }
    private func notification() -> MeowNotification {
        MeowNotification(
            id: "ntf_latency", type: .info, title: "Verified", body: "Verified body", source: "fixture",
            priority: "normal", pushStatus: "accepted", createdAt: .now)
    }

    func testVerifiedBodyDoesNotWaitForCancellationIgnoringStorageAndReceipt() async {
        let counts = AuxiliaryGate()
        let report = AuxiliaryGate()
        let capture = LatencyCapture()
        let countsStarted = expectation(description: "counts started")
        let reportStarted = expectation(description: "report started")
        let shown = expectation(description: "content shown within metadata budget")
        let n = notification()
        let identity = identity
        let delivery = NotificationDelivery(
            original: wake(), currentIdentity: { identity }, fetch: { _, _ in n },
            report: { _, _ in
                reportStarted.fulfill()
                await report.wait()
            },
            record: { _, _ in
                countsStarted.fulfill()
                await counts.wait()
            },
            counts: { _ in 999 },
            handler: {
                capture.append($0)
                shown.fulfill()
            })
        delivery.start()
        await fulfillment(of: [countsStarted, reportStarted, shown], timeout: 1)
        XCTAssertEqual(capture.values.count, 1)
        XCTAssertEqual(capture.values.first?.body, "Verified body")
        XCTAssertNil(capture.values.first?.badge)
        counts.release()
        report.release()
        try? await Task.sleep(for: .milliseconds(30))
        delivery.expire()
        delivery.expire()
        XCTAssertEqual(capture.values.count, 1, "late metadata/expiry must not redisplay or alter the result")
        XCTAssertNil(capture.values.first?.badge)
    }

    func testFastAuxiliariesApplyBadgeAndReport() async {
        let shown = expectation(description: "fast display")
        let reported = expectation(description: "reported")
        let identity = identity
        let n = notification()
        let capture = LatencyCapture()
        let delivery = NotificationDelivery(
            original: wake(), currentIdentity: { identity }, fetch: { _, _ in n },
            report: { _, _ in reported.fulfill() }, counts: { _ in 7 },
            handler: {
                capture.append($0)
                shown.fulfill()
            })
        delivery.start()
        await fulfillment(of: [shown, reported], timeout: 1)
        XCTAssertEqual(capture.values.first?.badge, 7)
        XCTAssertEqual(capture.values.count, 1)
    }
}
