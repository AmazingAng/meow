import XCTest
@testable import Meow

private final class OnboardingProtocol: URLProtocol, @unchecked Sendable {
    static let lock = NSLock()
    static var handler: ((URLRequest) throws -> Data)?
    static func install(_ handler: @escaping (URLRequest) throws -> Data) {
        lock.withLock { Self.handler = handler }
        URLProtocol.registerClass(Self.self)
    }
    static func uninstall() {
        URLProtocol.unregisterClass(Self.self)
        lock.withLock { handler = nil }
    }
    override class func canInit(with request: URLRequest) -> Bool {
        request.url?.host == "onboarding.invalid"
    }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        DispatchQueue.global().async {
            do {
                guard let handler = Self.lock.withLock({ Self.handler }) else { throw URLError(.cancelled) }
                let data = try handler(self.request)
                self.client?.urlProtocol(self, didReceive: HTTPURLResponse(url: self.request.url!, statusCode: 200,
                    httpVersion: nil, headerFields: nil)!, cacheStoragePolicy: .notAllowed)
                self.client?.urlProtocol(self, didLoad: data)
                self.client?.urlProtocolDidFinishLoading(self)
            } catch { self.client?.urlProtocol(self, didFailWithError: error) }
        }
    }
    override func stopLoading() {}
}

@MainActor
final class OnboardingDetectionTests: XCTestCase {
    private let server = URL(string: "https://onboarding.invalid")!
    private let token = "mdt_onboarding_test"
    private var oldServer: URL?
    private var oldToken: String?
    private var oldKey: String?
    private var oldTests: Any?
    private var oldOnboarded = false
    private var folder: URL!
    private var store: InboxStore!

    override func setUp() async throws {
        oldServer = MeowShared.server; oldToken = MeowShared.deviceToken
        oldKey = Keychain.get("device_key")
        oldTests = MeowShared.defaults.object(forKey: "onboarding_sent_tests")
        oldOnboarded = MeowShared.defaults.bool(forKey: "onboarded")
        MeowShared.server = server; MeowShared.deviceToken = token
        Keychain.set("meow_onboarding_test", for: "device_key")
        MeowShared.defaults.removeObject(forKey: "onboarding_sent_tests")
        MeowShared.defaults.set(false, forKey: "onboarded")
        folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
    }
    override func tearDown() async throws {
        OnboardingProtocol.uninstall()
        MeowShared.server = oldServer; MeowShared.deviceToken = oldToken
        Keychain.set(oldKey, for: "device_key")
        MeowShared.defaults.set(oldTests, forKey: "onboarding_sent_tests")
        MeowShared.defaults.set(oldOnboarded, forKey: "onboarded")
        try? FileManager.default.removeItem(at: folder)
    }
    private func item(_ id: String) -> MeowNotification {
        MeowNotification(id: id, type: .info, title: "Setup test", body: "Hello", source: "computer",
            priority: "normal", pushStatus: "accepted", createdAt: .now)
    }
    private var responseEncoder: JSONEncoder {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        encoder.keyEncodingStrategy = .convertToSnakeCase
        return encoder
    }
    private func mock(_ items: [MeowNotification], posted: MeowNotification? = nil) throws {
        let inbox = try responseEncoder.encode(InboxPage(notifications: items))
        let sent = try posted.map { try responseEncoder.encode($0) }
        OnboardingProtocol.install { request in
            if request.httpMethod == "POST", let sent { return sent }
            if request.url!.path.hasSuffix("/counts") { return Data(#"{"unread":0,"pending":0}"#.utf8) }
            return inbox
        }
    }

    func testComputerTestAlreadyReceivedBeforeFinalStepIsDetected() async throws {
        try mock([item("early")])
        let model = AppModel(inboxStore: store)
        await model.refresh() // Push / foreground refresh during step three.
        XCTAssertEqual(model.onboardingNotification?.id, "early", model.loadError ?? "")
        await model.refresh() // Same inbox during step four must still count.
        XCTAssertEqual(model.onboardingNotification?.id, "early", model.loadError ?? "")
    }

    func testComputerTestDetectedAfterRestartWithoutAnotherPush() async throws {
        let computer = item("before-restart")
        try await store.save([computer], scope: InboxStore.scope(server: server, token: token))
        OnboardingProtocol.install { _ in throw URLError(.notConnectedToInternet) }
        let restarted = AppModel(inboxStore: store)
        await restarted.refresh()
        XCTAssertEqual(restarted.onboardingNotification, computer)
        XCTAssertNotNil(restarted.loadError, "saved receipt remains evidence even if a later refresh is offline")
    }

    func testEmptyInboxDoesNotClaimConnected() async throws {
        try mock([])
        let model = AppModel(inboxStore: store)
        await model.refresh()
        XCTAssertNil(model.onboardingNotification)
    }

    func testPhoneTestExcludedBeforeAndAfterRestart() async throws {
        let phone = item("phone-test")
        try mock([phone], posted: phone)
        let model = AppModel(inboxStore: store)
        await model.sendTestNotification()
        XCTAssertEqual(model.savedNotifications.map(\.id), [phone.id])
        XCTAssertNil(model.onboardingNotification)
        let restarted = AppModel(inboxStore: store)
        await restarted.refresh()
        XCTAssertNil(restarted.onboardingNotification)
        XCTAssertTrue(restarted.sentTestIDs.contains(phone.id))
        try mock([item("computer"), phone])
        await restarted.refresh()
        XCTAssertEqual(restarted.onboardingNotification?.id, "computer")
    }

    func testPhoneTestInFlightCannotBeMistakenForComputerPush() async throws {
        let phone = item("in-flight-phone")
        let inbox = try responseEncoder.encode(InboxPage(notifications: [phone]))
        let sent = try responseEncoder.encode(phone)
        let started = expectation(description: "phone POST started")
        let gate = DispatchSemaphore(value: 0)
        defer { gate.signal() }
        OnboardingProtocol.install { request in
            if request.httpMethod == "POST" {
                started.fulfill()
                _ = gate.wait(timeout: .now() + 5)
                return sent
            }
            return request.url!.path.hasSuffix("/counts") ? Data(#"{"unread":1,"pending":0}"#.utf8) : inbox
        }
        let model = AppModel(inboxStore: store)
        let sending = Task { await model.sendTestNotification() }
        await fulfillment(of: [started], timeout: 3)
        await model.refresh() // A push beats the POST response.
        XCTAssertEqual(model.savedNotifications.first?.id, phone.id)
        XCTAssertNil(model.onboardingNotification)
        gate.signal()
        await sending.value
        XCTAssertNil(model.onboardingNotification)
        XCTAssertTrue(model.sentTestIDs.contains(phone.id))
    }

    func testFailedPhoneTestDoesNotBlockLaterComputerReceipt() async throws {
        OnboardingProtocol.install { _ in throw URLError(.notConnectedToInternet) }
        let model = AppModel(inboxStore: store)
        await model.sendTestNotification()
        XCTAssertNotNil(model.alert)
        try mock([item("later-computer")])
        await model.refresh()
        XCTAssertEqual(model.onboardingNotification?.id, "later-computer")
    }

    func testDetectionUsesCompleteInboxRegardlessOfSearchFilterAndDisplayPage() async throws {
        let phones = (0..<60).map { item("phone-\($0)") }
        MeowShared.defaults.set(["scope": InboxStore.scope(server: server, token: token),
                                "ids": phones.map(\.id)], forKey: "onboarding_sent_tests")
        var computer = item("older-computer")
        computer.createdAt = .distantPast
        try mock(phones + [computer])
        let model = AppModel(inboxStore: store)
        await model.refresh()
        XCTAssertFalse(model.notifications.contains { $0.id == computer.id })
        model.search = "no matching message"
        model.filter = .pending
        XCTAssertTrue(model.notifications.isEmpty)
        XCTAssertEqual(model.onboardingNotification?.id, computer.id)
    }

    func testSelfTestExclusionIsScopedToServerAndDevice() async throws {
        for foreignScope in [InboxStore.scope(server: URL(string: "https://other.invalid")!, token: token),
                             InboxStore.scope(server: server, token: "mdt_other_test")] {
            MeowShared.defaults.set(["scope": foreignScope, "ids": ["same-id"]], forKey: "onboarding_sent_tests")
            try mock([item("same-id")])
            let model = AppModel(inboxStore: store)
            await model.refresh()
            XCTAssertEqual(model.onboardingNotification?.id, "same-id")
        }
    }
}
