import XCTest
import UserNotifications
@testable import Meow

final class GatewayContentTests: XCTestCase {
    let identity = NotificationIdentity(server: URL(string: "https://my.example")!, token: "mdt_test", deviceID: "dev_1", bindingID: "bind_1")

    func wake(binding: String = "bind_1") -> UNMutableNotificationContent {
        let c = UNMutableNotificationContent()
        c.title = "Untrusted title"
        c.body = "Untrusted body"
        c.categoryIdentifier = NotificationCategory.approval
        c.userInfo = ["meow": ["id": "ntf_1", "relay": true, "binding_id": binding,
                              "question_id": "qst_forged", "choices": ["approve", "reject"]]]
        return c
    }

    func notification(status: QuestionStatus = .pending, choices: [String]? = ["Approve", "Reject"], expires: Date = .now.addingTimeInterval(60)) -> MeowNotification {
        MeowNotification(id: "ntf_1", type: .question, title: "Deploy?", body: "Production", source: "Codex",
                         priority: "high", pushStatus: "accepted", createdAt: .now,
                         question: .init(id: "qst_real", status: status, choices: choices, expiresAt: expires))
    }

    func testRelayRequiresCurrentBindingAndSafeIdentifier() {
        XCTAssertTrue(identity.accepts(["id": "ntf_1", "relay": true, "binding_id": "bind_1"]))
        for m: [String: Any] in [
            ["id": "ntf_1", "relay": true, "binding_id": "old"],
            ["id": "ntf_1", "relay": true],
            ["id": "../v1/keys", "relay": true, "binding_id": "bind_1"],
            ["id": "ntf_1", "device_id": "dev_other"],
        ] { XCTAssertFalse(identity.accepts(m)) }
        XCTAssertTrue(identity.accepts(["id": "ntf_1", "device_id": "dev_1"]))
        XCTAssertFalse(identity.accepts(["id": "ntf_legacy"]), "legacy notifications without identity cannot target the current server")
        var switched = identity
        switched.bindingID = nil
        XCTAssertFalse(switched.accepts(["id": "ntf_1", "relay": true, "binding_id": "bind_1"]))
    }

    func testFallbackHasNoActionsOrQuestionFields() throws {
        let result = NotificationContent.fallback(wake(), identity: identity)
        XCTAssertEqual(result.categoryIdentifier, "")
        XCTAssertEqual(result.title, "Meow")
        XCTAssertFalse(result.body.contains("Untrusted"))
        let m = try XCTUnwrap(result.userInfo["meow"] as? [String: Any])
        XCTAssertNil(m["question_id"])
        XCTAssertNil(m["choices"])
        XCTAssertNil(m["hydrated"])
        let stale = NotificationContent.fallback(wake(binding: "old"), identity: identity)
        XCTAssertNil(stale.userInfo["meow"], "stale wakes must not open a new server's notification")
    }

    func testHydrateUsesOnlyAuthoritativeContent() throws {
        let result = try XCTUnwrap(NotificationContent.hydrate(notification(), original: wake(), identity: identity))
        XCTAssertEqual(result.title, "Deploy?")
        XCTAssertEqual(result.subtitle, "Codex")
        XCTAssertEqual(result.body, "Production")
        XCTAssertEqual(result.categoryIdentifier, NotificationCategory.approval)
        XCTAssertEqual(result.threadIdentifier, "Codex")
        let m = try XCTUnwrap(result.userInfo["meow"] as? [String: Any])
        XCTAssertEqual(m["question_id"] as? String, "qst_real")
        XCTAssertEqual(m["choices"] as? [String], ["Approve", "Reject"])
        XCTAssertEqual(m["hydrated"] as? Bool, true)
        var wrong = notification(); wrong.id = "ntf_other"
        XCTAssertNil(NotificationContent.hydrate(wrong, original: wake(), identity: identity))
        XCTAssertNil(NotificationContent.hydrate(notification(), original: wake(binding: "old"), identity: identity))
    }

    func testHydrationUsesOriginPriorityInsteadOfWakeInterruptionLevel() throws {
        for (priority, level, silent) in [
            ("high", UNNotificationInterruptionLevel.timeSensitive, false),
            ("normal", .active, false),
            ("low", .passive, true),
        ] {
            var n = notification()
            n.priority = priority
            let original = wake()
            // A mismatched wake must neither silence urgent messages nor
            // elevate a normal/quiet message into an urgent one.
            original.interruptionLevel = priority == "high" ? .passive : .timeSensitive
            original.sound = priority == "high" ? nil : .default
            let result = try XCTUnwrap(NotificationContent.hydrate(n, original: original, identity: identity))
            XCTAssertEqual(result.interruptionLevel, level, priority)
            XCTAssertEqual(result.sound == nil, silent, priority)
        }
    }

    func testResolvedExpiredAndCustomChoiceHaveNoBackgroundApproval() throws {
        for status: QuestionStatus in [.answered, .rejected, .expired, .cancelled] {
            let result = try XCTUnwrap(NotificationContent.hydrate(notification(status: status), original: wake(), identity: identity))
            XCTAssertEqual(result.categoryIdentifier, NotificationCategory.info)
            XCTAssertNil((result.userInfo["meow"] as? [String: Any])?["question_id"])
        }
        let expired = try XCTUnwrap(NotificationContent.hydrate(notification(expires: .now.addingTimeInterval(-1)), original: wake(), identity: identity))
        XCTAssertEqual(expired.categoryIdentifier, NotificationCategory.info)
        let custom = try XCTUnwrap(NotificationContent.hydrate(notification(choices: ["postgres", "sqlite"]), original: wake(), identity: identity))
        XCTAssertEqual(custom.categoryIdentifier, NotificationCategory.choice)
        let text = try XCTUnwrap(NotificationContent.hydrate(notification(choices: nil), original: wake(), identity: identity))
        XCTAssertEqual(text.categoryIdentifier, NotificationCategory.reply)
    }
}

extension GatewayContentTests {
    func testOnlyHydratedUnexpiredCurrentRelayCanRespond() throws {
        let unsafe = wake().userInfo["meow"] as! [String: Any]
        XCTAssertFalse(NotificationContent.canRespond(unsafe, identity: identity))
        let content = try XCTUnwrap(NotificationContent.hydrate(notification(), original: wake(), identity: identity))
        let verified = content.userInfo["meow"] as! [String: Any]
        XCTAssertTrue(NotificationContent.canRespond(verified, identity: identity))
        XCTAssertFalse(NotificationContent.canRespond(verified, identity: identity, now: .now.addingTimeInterval(120)))
        var changed = identity; changed.bindingID = "new_registration"
        XCTAssertFalse(NotificationContent.canRespond(verified, identity: changed))
        let direct: [String: Any] = ["id": "ntf_1", "device_id": "dev_1", "question_id": "qst_1", "expires_at": "2000-01-01T00:00:00Z"]
        XCTAssertFalse(NotificationContent.canRespond(direct, identity: identity))
    }

    func testGatewayURLsSupportSelfHostingAndRejectEmbeddedCredentials() {
        for raw in ["https://user:password@push.example", "https://push.example?q=secret", "file:///tmp", "https://push.example#fragment"] {
            XCTAssertNil(MeowTransport.gatewayURL(raw))
        }
        XCTAssertEqual(MeowTransport.gatewayURL("https://push.example/prefix/")?.absoluteString, "https://push.example/prefix")
        XCTAssertNotNil(MeowTransport.gatewayURL("http://127.0.0.1:8080"))
        XCTAssertNotNil(MeowTransport.gatewayURL("http://192.168.31.235:8080"))
        XCTAssertNotNil(MeowTransport.gatewayURL("http://localhost:8080"))
    }
}

private final class DeliveryCapture: @unchecked Sendable {
    private let lock = NSLock()
    private var saved: [UNNotificationContent] = []
    private var identity: NotificationIdentity?
    init(identity: NotificationIdentity?) { self.identity = identity }
    func append(_ content: UNNotificationContent) { lock.withLock { saved.append(content) } }
    var values: [UNNotificationContent] { lock.withLock { saved } }
    func current() -> NotificationIdentity? { lock.withLock { identity } }
    func switchIdentity() { lock.withLock { identity = nil } }
}

private actor HeldNotification {
    let started: XCTestExpectation
    private var continuation: CheckedContinuation<MeowNotification, Error>?
    init(started: XCTestExpectation) { self.started = started }
    func fetch() async throws -> MeowNotification {
        try await withCheckedThrowingContinuation {
            continuation = $0
            started.fulfill()
        }
    }
    func finish(_ n: MeowNotification) { continuation?.resume(returning: n); continuation = nil }
}

extension GatewayContentTests {
    func testExtensionExpiryReturnsExactlyOnceAndIgnoresLateFetch() async throws {
        let started = expectation(description: "fetch started")
        let finished = expectation(description: "handler called")
        let held = HeldNotification(started: started)
        let capture = DeliveryCapture(identity: identity)
        let delivery = NotificationDelivery(original: wake(), currentIdentity: { capture.current() }, fetch: { _, _ in
            try await held.fetch()
        }, report: { _, _ in XCTFail("expired extension must not report delivery after a late fetch") }, handler: {
            capture.append($0); finished.fulfill()
        })
        delivery.start()
        await fulfillment(of: [started], timeout: 3)
        delivery.expire()
        delivery.expire()
        await fulfillment(of: [finished], timeout: 3)
        await held.finish(notification())
        try await Task.sleep(for: .milliseconds(80))
        XCTAssertEqual(capture.values.count, 1)
        XCTAssertEqual(capture.values.first?.categoryIdentifier, "")
        XCTAssertNil((capture.values.first?.userInfo["meow"] as? [String: Any])?["question_id"])
    }

    func testIdentityChangeDuringFetchCannotDisplayOldQuestion() async throws {
        let started = expectation(description: "fetch started")
        let finished = expectation(description: "handler called")
        let held = HeldNotification(started: started)
        let capture = DeliveryCapture(identity: identity)
        let delivery = NotificationDelivery(original: wake(), currentIdentity: { capture.current() }, fetch: { _, _ in
            try await held.fetch()
        }, report: { _, _ in XCTFail("old server identity must not report") }, handler: {
            capture.append($0); finished.fulfill()
        })
        delivery.start()
        await fulfillment(of: [started], timeout: 3)
        capture.switchIdentity()
        await held.finish(notification())
        await fulfillment(of: [finished], timeout: 3)
        XCTAssertEqual(capture.values.first?.categoryIdentifier, "")
        XCTAssertNil(capture.values.first?.userInfo["meow"])
    }

    func testFailedFetchHasNoActionsAndNoDeliveryReport() async throws {
        let finished = expectation(description: "fallback")
        let capture = DeliveryCapture(identity: identity)
        let delivery = NotificationDelivery(original: wake(), currentIdentity: { capture.current() }, fetch: { _, _ in
            throw URLError(.timedOut)
        }, report: { _, _ in XCTFail("failed fetch must not claim delivery") }, handler: {
            capture.append($0); finished.fulfill()
        })
        delivery.start()
        await fulfillment(of: [finished], timeout: 3)
        XCTAssertEqual(capture.values.first?.categoryIdentifier, "")
    }

    func testSuccessfulFetchSurvivesReceiptFailureAndUpdatesBadge() async throws {
        let finished = expectation(description: "hydrated")
        let capture = DeliveryCapture(identity: identity)
        let n = notification()
        let delivery = NotificationDelivery(original: wake(), currentIdentity: { capture.current() }, fetch: { id, snapshot in
            XCTAssertEqual(id, "ntf_1")
            XCTAssertEqual(snapshot.server.absoluteString, "https://my.example")
            XCTAssertEqual(snapshot.token, "mdt_test")
            return n
        }, report: { _, _ in throw URLError(.notConnectedToInternet) }, counts: { _ in 7 }, handler: {
            capture.append($0); finished.fulfill()
        })
        delivery.start()
        await fulfillment(of: [finished], timeout: 3)
        XCTAssertEqual(capture.values.first?.categoryIdentifier, NotificationCategory.approval)
        XCTAssertEqual(capture.values.first?.badge, 7)
        delivery.expire()
        XCTAssertEqual(capture.values.count, 1)
    }
}

extension GatewayContentTests {
    func testDirectNotificationReportsReceiptWithoutFetching() async throws {
        let finished = expectation(description: "direct delivery")
        let reported = expectation(description: "receipt")
        let c = UNMutableNotificationContent()
        c.title = "Existing direct alert"
        c.categoryIdentifier = NotificationCategory.approval
        c.userInfo = ["meow": ["id": "ntf_direct", "device_id": "dev_1"]]
        let capture = DeliveryCapture(identity: identity)
        let delivery = NotificationDelivery(original: c, currentIdentity: { capture.current() }, fetch: { _, _ in
            XCTFail("direct pushes already contain their message")
            throw URLError(.badServerResponse)
        }, report: { id, snapshot in
            XCTAssertEqual(id, "ntf_direct")
            XCTAssertEqual(snapshot.token, "mdt_test")
            reported.fulfill()
        }, handler: { capture.append($0); finished.fulfill() })
        delivery.start()
        await fulfillment(of: [reported, finished], timeout: 3)
        XCTAssertEqual(capture.values.first?.title, "Existing direct alert")
        XCTAssertEqual(capture.values.first?.categoryIdentifier, NotificationCategory.approval)
    }

    func testOrdinaryRelayHasNoReplyAndTruncatesLongBody() throws {
        var n = notification()
        n.type = .info
        n.question = nil
        n.source = ""
        n.body = String(repeating: "猫", count: 1500)
        let c = try XCTUnwrap(NotificationContent.hydrate(n, original: wake(), identity: identity))
        XCTAssertEqual(c.title, "Deploy?")
        XCTAssertEqual(c.body.count, 180)
        XCTAssertTrue(c.body.hasSuffix("…"))
        XCTAssertEqual(c.categoryIdentifier, NotificationCategory.info)
        XCTAssertNil((c.userInfo["meow"] as? [String: Any])?["question_id"])
    }
}

extension GatewayContentTests {
    func testQuestionExpiringDuringReceiptCannotKeepActionsAtDisplay() throws {
        let now = Date()
        let c = try XCTUnwrap(NotificationContent.hydrate(notification(expires: now.addingTimeInterval(1)), original: wake(), identity: identity, now: now))
        XCTAssertEqual(c.categoryIdentifier, NotificationCategory.approval)
        let expired = NotificationContent.forDisplay(c, identity: identity, now: now.addingTimeInterval(2))
        XCTAssertEqual(expired.categoryIdentifier, NotificationCategory.info)
        XCTAssertNil((expired.userInfo["meow"] as? [String: Any])?["question_id"])
        XCTAssertEqual(expired.body, c.body, "verified content remains useful after the question expires")
        let current = NotificationContent.forDisplay(c, identity: identity, now: now)
        XCTAssertEqual(current.categoryIdentifier, NotificationCategory.approval)
    }
}
