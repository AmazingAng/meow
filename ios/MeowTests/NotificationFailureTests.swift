import XCTest
import UserNotifications
@testable import Meow

private final class FailureProtocol: URLProtocol, @unchecked Sendable {
    static var response = (200, Data())
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: Self.response.0,
                            httpVersion: nil, headerFields: ["Content-Type": "application/json"])!, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Self.response.1)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

private actor NotificationGate {
    let started: XCTestExpectation
    private var continuation: CheckedContinuation<Void, Never>?
    init(_ started: XCTestExpectation) { self.started = started }
    func wait() async {
        await withCheckedContinuation { continuation = $0; started.fulfill() }
    }
    func release() { continuation?.resume(); continuation = nil }
}

private final class FailureCapture: @unchecked Sendable {
    private let lock = NSLock()
    private var identity: NotificationIdentity?
    private var contents: [UNNotificationContent] = []
    init(_ identity: NotificationIdentity?) { self.identity = identity }
    func current() -> NotificationIdentity? { lock.withLock { identity } }
    func change(to identity: NotificationIdentity?) { lock.withLock { self.identity = identity } }
    func append(_ content: UNNotificationContent) { lock.withLock { contents.append(content) } }
    var values: [UNNotificationContent] { lock.withLock { contents } }
}

extension GatewayContentTests {
    func testOldDirectAndUnidentifiedNotificationsHaveNoActionsOrDestination() throws {
        for device: Any? in ["dev_old", nil, 123] {
            let c = UNMutableNotificationContent()
            c.title = "Old private title"; c.body = "Old private body"
            c.categoryIdentifier = NotificationCategory.approval
            c.threadIdentifier = "Old server"
            c.badge = 99
            var m: [String: Any] = ["id": "ntf_old", "question_id": "qst_old"]
            m["device_id"] = device
            c.userInfo = ["meow": m]
            XCTAssertFalse(identity.accepts(m), "direct notifications need the current device identity")
            let fallback = NotificationContent.fallback(c, identity: identity)
            XCTAssertEqual(fallback.title, "Meow")
            XCTAssertFalse(fallback.body.contains("private"))
            XCTAssertEqual(fallback.categoryIdentifier, "")
            XCTAssertEqual(fallback.threadIdentifier, "")
            XCTAssertNil(fallback.badge)
            XCTAssertTrue(fallback.userInfo.isEmpty)
            let displayed = NotificationContent.forDisplay(c, identity: identity)
            XCTAssertTrue(displayed.userInfo.isEmpty)
            XCTAssertEqual(displayed.categoryIdentifier, "")
        }
    }

    func testDirectExpiryIsRecheckedAtDisplay() throws {
        let c = UNMutableNotificationContent()
        c.body = "Verified direct content"
        c.categoryIdentifier = NotificationCategory.approval
        c.userInfo = ["meow": ["id": "ntf_direct", "device_id": "dev_1", "question_id": "qst_1",
                              "expires_at": "2000-01-01T00:00:00Z", "choices": ["approve", "reject"]]]
        let displayed = NotificationContent.forDisplay(c, identity: identity)
        XCTAssertEqual(displayed.body, c.body)
        XCTAssertEqual(displayed.categoryIdentifier, NotificationCategory.info)
        XCTAssertNil((displayed.userInfo["meow"] as? [String: Any])?["question_id"])
    }

    func testMissingIdentityNeverDisplaysOldContent() {
        let hydrated = NotificationContent.hydrate(notification(), original: wake(), identity: identity)!
        let displayed = NotificationContent.forDisplay(hydrated, identity: nil)
        XCTAssertEqual(displayed.title, "Meow")
        XCTAssertFalse(displayed.body.contains("Production"))
        XCTAssertTrue(displayed.userInfo.isEmpty)
        XCTAssertEqual(displayed.categoryIdentifier, "")
    }

    func testDelayedOldRelayAndDirectNeverFetchOrReportToNewIdentity() async {
        var next = identity
        next.server = URL(string: "https://next.example")!
        next.token = "mdt_next"; next.deviceID = "dev_next"; next.bindingID = "bind_next"
        let direct = UNMutableNotificationContent()
        direct.title = "Old direct question"
        direct.categoryIdentifier = NotificationCategory.approval
        direct.userInfo = ["meow": ["id": "ntf_1", "device_id": "dev_1", "question_id": "qst_old"]]
        for original in [wake(), direct] {
            let done = expectation(description: "stale wake discarded")
            let capture = FailureCapture(next)
            let delivery = NotificationDelivery(original: original, currentIdentity: { capture.current() }, fetch: { _, _ in
                XCTFail("never fetch an old notification from the new server"); throw URLError(.badURL)
            }, report: { _, _ in XCTFail("never report an old notification to the new server") }, handler: {
                capture.append($0); done.fulfill()
            })
            delivery.start()
            await fulfillment(of: [done], timeout: 3)
            XCTAssertEqual(capture.values.count, 1)
            XCTAssertEqual(capture.values.first?.title, "Meow")
            XCTAssertEqual(capture.values.first?.categoryIdentifier, "")
            XCTAssertEqual(capture.values.first?.userInfo.isEmpty, true)
        }
    }

    func testHTTPAndMalformedOriginResponsesKeepSafeRecoveryDestination() async throws {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [FailureProtocol.self]
        let session = URLSession(configuration: configuration)
        defer { session.invalidateAndCancel() }
        for (status, body) in [(401, "{}"), (404, "{}"), (500, "{}"), (503, "{}"),
                               (200, "not json"), (200, "{}"), (200, "null")] {
            FailureProtocol.response = (status, Data(body.utf8))
            let done = expectation(description: "safe fallback \(status) \(body)")
            let capture = FailureCapture(identity)
            let delivery = NotificationDelivery(original: wake(), currentIdentity: { capture.current() }, fetch: { id, snapshot in
                try await MeowAPI(server: snapshot.server, token: snapshot.token, session: session).notification(id)
            }, report: { _, _ in XCTFail("no receipt for an unverified fetch") }, handler: {
                capture.append($0); done.fulfill()
            })
            delivery.start()
            await fulfillment(of: [done], timeout: 3)
            let c = try XCTUnwrap(capture.values.first)
            XCTAssertEqual(c.categoryIdentifier, "")
            XCTAssertEqual(c.title, "Meow")
            let m = try XCTUnwrap(c.userInfo["meow"] as? [String: Any])
            XCTAssertEqual(m["id"] as? String, "ntf_1", "tap may retry this ID after recovery")
            XCTAssertNil(m["question_id"])
            XCTAssertNil(m["hydrated"])
        }
    }

    func testNetworkAndTLSFailuresAreSafeAndRecoveryHydratesAgain() async throws {
        for code: URLError.Code in [.notConnectedToInternet, .cannotConnectToHost, .timedOut, .secureConnectionFailed] {
            let done = expectation(description: "network fallback")
            let capture = FailureCapture(identity)
            let delivery = NotificationDelivery(original: wake(), currentIdentity: { capture.current() }, fetch: { _, _ in
                throw URLError(code)
            }, report: { _, _ in XCTFail("failed origin fetch must not report receipt") }, handler: {
                capture.append($0); done.fulfill()
            })
            delivery.start()
            await fulfillment(of: [done], timeout: 3)
            let fallback = try XCTUnwrap(capture.values.first)
            XCTAssertEqual(fallback.categoryIdentifier, "")
            let recovered = try XCTUnwrap(NotificationContent.hydrate(notification(), original: fallback, identity: identity))
            XCTAssertEqual(recovered.categoryIdentifier, NotificationCategory.approval)
            XCTAssertEqual(recovered.body, "Production")
        }
    }

    func testCountsAndReceiptFailuresKeepVerifiedContent() async throws {
        let done = expectation(description: "verified notification")
        let capture = FailureCapture(identity)
        let n = notification()
        let delivery = NotificationDelivery(original: wake(), currentIdentity: { capture.current() }, fetch: { _, _ in n },
            report: { _, _ in throw URLError(.timedOut) }, counts: { _ in throw URLError(.timedOut) }, handler: {
                capture.append($0); done.fulfill()
            })
        delivery.start()
        await fulfillment(of: [done], timeout: 3)
        XCTAssertEqual(capture.values.first?.body, "Production")
        XCTAssertEqual(capture.values.first?.categoryIdentifier, NotificationCategory.approval)
    }

    func testSwitchDuringCountsDropsOldBodyBadgeAndReceipt() async throws {
        let started = expectation(description: "counts in progress")
        let done = expectation(description: "generic after switch")
        let gate = NotificationGate(started)
        let capture = FailureCapture(identity)
        let n = notification()
        let original = wake()
        original.threadIdentifier = "old-thread"
        let delivery = NotificationDelivery(original: original, currentIdentity: { capture.current() }, fetch: { _, _ in n },
            report: { _, usedIdentity in
                XCTAssertEqual(capture.current(), usedIdentity, "receipt must begin only in its original registration")
            }, counts: { _ in
                await gate.wait(); return 99
            }, handler: { capture.append($0); done.fulfill() })
        delivery.start()
        await fulfillment(of: [started], timeout: 3)
        var next = identity; next.token = "mdt_next"; next.bindingID = "bind_next"
        capture.change(to: next)
        await gate.release()
        await fulfillment(of: [done], timeout: 3)
        let c = try XCTUnwrap(capture.values.first)
        XCTAssertEqual(c.title, "Meow")
        XCTAssertEqual(c.categoryIdentifier, "")
        XCTAssertEqual(c.threadIdentifier, "")
        XCTAssertNil(c.badge)
        XCTAssertTrue(c.userInfo.isEmpty)
    }

    func testWrongOriginIDNeverReportsReceipt() async {
        let done = expectation(description: "wrong ID fallback")
        let capture = FailureCapture(identity)
        var wrong = notification(); wrong.id = "ntf_other"
        let n = wrong
        let delivery = NotificationDelivery(original: wake(), currentIdentity: { capture.current() }, fetch: { _, _ in n },
            report: { _, _ in XCTFail("wrong ID was not verified") }, handler: { capture.append($0); done.fulfill() })
        delivery.start()
        await fulfillment(of: [done], timeout: 3)
        XCTAssertEqual(capture.values.first?.categoryIdentifier, "")
    }
}
