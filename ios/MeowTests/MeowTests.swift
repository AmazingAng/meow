import XCTest
@testable import Meow

final class ModelTests: XCTestCase {
    func testParsesGoTimestamps() throws {
        let cases: [(String, Double)] = [
            ("2026-10-17T06:28:11Z", 0),
            ("2026-10-17T06:28:11.1Z", 0.1),
            ("2026-10-17T06:28:11.123Z", 0.123),
            ("2026-10-17T06:28:11.123456789Z", 0.123456789),
            ("2026-10-17T14:28:11.5+08:00", 0.5),
        ]
        let base = try XCTUnwrap(MeowJSON.parseDate("2026-10-17T06:28:11Z"))
        for (s, fraction) in cases {
            let d = try XCTUnwrap(MeowJSON.parseDate(s), s)
            XCTAssertEqual(d.timeIntervalSince(base), fraction, accuracy: 1e-6, s)
        }
        XCTAssertNil(MeowJSON.parseDate("yesterday"))
    }

    func testDecodesInboxPage() throws {
        let json = """
        {"notifications":[{"id":"ntf_1","type":"question","title":"Deploy to production?","body":"services/api",
        "source":"Claude Code","priority":"high","push_status":"accepted","created_at":"2026-10-17T06:28:11.12Z",
        "delivered_at":null,"read_at":null,"question":{"id":"qst_1","status":"pending","choices":["approve","reject"],
        "answer":null,"expires_at":"2026-10-17T06:43:11.12Z"}}],"next_cursor":null}
        """
        let page = try MeowJSON.decoder.decode(InboxPage.self, from: Data(json.utf8))
        let n = try XCTUnwrap(page.notifications.first)
        XCTAssertEqual(n.type, .question)
        XCTAssertTrue(n.isUnread)
        XCTAssertEqual(n.question?.isApproval, true)
        XCTAssertNil(page.nextCursor)
    }

    func testQuestionOpenAndApproval() {
        let now = Date()
        var q = MeowNotification.Question(id: "q", status: .pending, choices: ["Reject", "APPROVE"], answer: nil,
                                          expiresAt: now.addingTimeInterval(60))
        XCTAssertTrue(q.isOpen(at: now))
        XCTAssertTrue(q.isApproval)
        XCTAssertFalse(q.isOpen(at: now.addingTimeInterval(61)))
        q.choices = ["postgres", "sqlite"]
        XCTAssertFalse(q.isApproval)
        q.choices = nil
        XCTAssertFalse(q.isApproval)
        q.status = .answered
        XCTAssertFalse(q.isOpen(at: now))
    }

    func testAvatarInitialsAndStableColor() {
        XCTAssertEqual(AvatarStyle.initials(for: "Claude Code"), "CC")
        XCTAssertEqual(AvatarStyle.initials(for: "codex"), "C")
        XCTAssertEqual(AvatarStyle.initials(for: "github-actions"), "GA")
        XCTAssertEqual(AvatarStyle.initials(for: "部署机器人"), "部")
        XCTAssertEqual(AvatarStyle.initials(for: ""), "")
        XCTAssertEqual(AvatarStyle.colorIndex(for: "Codex"), AvatarStyle.colorIndex(for: "codex"))
        XCTAssertTrue((0..<AvatarStyle.palette.count).contains(AvatarStyle.colorIndex(for: "Claude Code")))
    }

    func testCountdownFormat() {
        XCTAssertEqual(ExpiryCountdown.format(65), "1:05")
        XCTAssertEqual(ExpiryCountdown.format(3725), "1:02:05")
    }
}

/// Holds network responses at the URLSession boundary, never replaces AppModel logic.
private final class ReviewURLProtocol: URLProtocol, @unchecked Sendable {
    private static let lock = NSLock()
    private static var callback: ((URLRequest) -> (Int, Data))?

    static func install(_ handler: @escaping (URLRequest) -> (Int, Data)) {
        lock.withLock { callback = handler }
        URLProtocol.registerClass(Self.self)
    }
    static func uninstall() {
        URLProtocol.unregisterClass(Self.self)
        lock.withLock { callback = nil }
    }
    override class func canInit(with request: URLRequest) -> Bool {
        request.url?.host?.hasSuffix(".meow-review.invalid") == true
    }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        guard let handler = Self.lock.withLock({ Self.callback }) else {
            client?.urlProtocol(self, didFailWithError: URLError(.cancelled))
            return
        }
        let (status, data) = handler(request)
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil,
                                       headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@MainActor
final class SessionRegressionTests: XCTestCase {
    private let serverA = URL(string: "https://a.meow-review.invalid")!
    private let serverB = URL(string: "https://b.meow-review.invalid")!

    private struct Snapshot {
        let server = MeowShared.server
        let token = MeowShared.deviceToken
        let onboarded = MeowShared.defaults.bool(forKey: "onboarded")
        let key = Keychain.get("device_key")
        let keyID = MeowShared.defaults.string(forKey: "device_key_id")
        func restore() {
            MeowShared.server = server
            MeowShared.deviceToken = token
            MeowShared.defaults.set(onboarded, forKey: "onboarded")
            Keychain.set(key, for: "device_key")
            MeowShared.defaults.set(keyID, forKey: "device_key_id")
        }
    }

    private var registrationJSON: Data {
        Data("""
        {"device":{"id":"dev_review","name":"Test","apns_env":"production","has_apns_token":false,
        "created_at":"2026-10-17T00:00:00Z","last_seen_at":"2026-10-17T00:00:00Z"},
        "device_token":"mdt_review_A","key":{"id":"key_review","name":"Default","hint":"meow_…",
        "scopes":["notify","ask"],"created_at":"2026-10-17T00:00:00Z","last_used_at":null,"secret":"meow_review"}}
        """.utf8)
    }


    func testServerCannotChangeDuringRegistration() async {
        let snapshot = Snapshot()
        defer { snapshot.restore(); ReviewURLProtocol.uninstall() }
        MeowShared.server = serverA
        MeowShared.deviceToken = nil
        let started = expectation(description: "registration started")
        let gate = DispatchSemaphore(value: 0)
        let json = registrationJSON
        ReviewURLProtocol.install { _ in
            started.fulfill()
            _ = gate.wait(timeout: .now() + 10)
            return (201, json)
        }
        let model = AppModel()
        let registration = Task { await model.register() }
        await fulfillment(of: [started], timeout: 5)
        XCTAssertTrue(model.isRegistering)
        model.setServer(serverB)
        XCTAssertEqual(model.server, serverA, "a live registration cannot change its destination")
        gate.signal()
        await registration.value
        XCTAssertTrue(model.isRegistered)
        XCTAssertEqual(MeowShared.server, serverA)
        XCTAssertEqual(MeowShared.deviceToken, "mdt_review_A")
    }

    func testOldInboxResponseCannotRepopulateAfterSwitch() async {
        let snapshot = Snapshot()
        defer { snapshot.restore(); ReviewURLProtocol.uninstall() }
        MeowShared.server = serverA
        MeowShared.deviceToken = "mdt_review_A"
        let started = expectation(description: "old inbox requested")
        let gate = DispatchSemaphore(value: 0)
        ReviewURLProtocol.install { req in
            if req.url!.path == "/v1/inbox" {
                started.fulfill()
                _ = gate.wait(timeout: .now() + 10)
                return (200, Data("{\"notifications\":[],\"next_cursor\":null}".utf8))
            }
            return (200, Data("{\"unread\":7,\"pending\":2}".utf8))
        }
        let file = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        let queue = ReplyQueue(fileURL: file)
        let model = AppModel(replies: queue)
        let refresh = Task { await model.refresh() }
        await fulfillment(of: [started], timeout: 5)
        await model.reset(server: serverB)
        gate.signal()
        await refresh.value
        XCTAssertEqual(model.server, serverB)
        XCTAssertFalse(model.hasLoaded, "a response for the old identity must be ignored")
        XCTAssertEqual(model.counts.unread, 0)
    }
}


extension SessionRegressionTests {
    func testFinishOnboardingLoadsInboxWithoutSceneTransition() async {
        let snapshot = Snapshot()
        defer { snapshot.restore(); ReviewURLProtocol.uninstall() }
        MeowShared.server = serverA
        MeowShared.deviceToken = "mdt_review_A"
        ReviewURLProtocol.install { req in
            if req.url!.path == "/v1/inbox" {
                return (200, Data("{\"notifications\":[],\"next_cursor\":null}".utf8))
            }
            return (200, Data("{\"unread\":0,\"pending\":0}".utf8))
        }
        let model = AppModel()
        await model.finishOnboarding()
        XCTAssertTrue(model.hasFinishedOnboarding)
        XCTAssertTrue(model.hasLoaded)
        XCTAssertNil(model.loadError)
    }
}


extension SessionRegressionTests {


}




final class OnboardingCommandTests: XCTestCase {
    func testMaskedDeviceKeyShowsOnlyPrefixAndLastFour() {
        XCTAssertEqual(AppModel.maskedDeviceKey("meow_abcdefghijklmnopWXYZ"), "meow_…WXYZ")
        XCTAssertEqual(AppModel.maskedDeviceKey("abcdefghWXYZ"), "…WXYZ")
        XCTAssertFalse(AppModel.maskedDeviceKey("meow_abcdefghijklmnopWXYZ").contains("abcd"))
    }

    func testScriptKeyPromptHidesInputAndKeepsStdoutForCurl() {
        let command = AppModel.scriptKeyInputCommand
        XCTAssertTrue(command.contains("read -rs MEOW_KEY"))
        XCTAssertFalse(command.contains("meow_"))
        // curl's JSON response is the only thing on stdout.
        XCTAssertEqual(command.components(separatedBy: ">&2").count - 1, 2)
    }

    func testScriptExampleUsesVariableAndQuotesServer() throws {
        let server = try XCTUnwrap(URL(string: "https://meow.example.com/o'neil/"))
        let command = AppModel.scriptNotificationCommand(server: server)
        XCTAssertEqual(command, #"curl -fsS -H "Authorization: Bearer $MEOW_KEY" -d "Hello from curl" 'https://meow.example.com/o'\''neil/v1/notifications'"#)
        XCTAssertFalse(command.contains("meow_"))
    }

    func testInstallAndSetupCommandKeepsServerQuotedAndKeyOutOfHistory() throws {
        let server = try XCTUnwrap(URL(string: "https://meow.example.com/o'neil/"))
        XCTAssertEqual(AppModel.installAndSetupCommand(server: server),
                       AppModel.cliInstallCommand + #" -s -- --server 'https://meow.example.com/o'\''neil'"#)
        XCTAssertEqual(AppModel.installAndSetupCommand(server: nil), AppModel.cliInstallCommand)
        XCTAssertFalse(AppModel.installAndSetupCommand(server: server).contains("meow_"))
        XCTAssertTrue(AppModel.cliInstallCommand.hasSuffix(" | sh"))
    }

    func testSetupCommandAlwaysNamesTheServer() throws {
        let server = try XCTUnwrap(URL(string: "https://meow.example.com/"))
        XCTAssertEqual(AppModel.setupCommand(server: server), "meow setup --server https://meow.example.com")
        let local = try XCTUnwrap(URL(string: "http://127.0.0.1:8080"))
        XCTAssertEqual(AppModel.setupCommand(server: local), "meow setup --server http://127.0.0.1:8080")
    }

    func testSetupCommandQuotesUnsafeURLs() throws {
        let quote = try XCTUnwrap(URL(string: "https://meow.example.com/o'neil"))
        XCTAssertEqual(AppModel.setupCommand(server: quote), #"meow setup --server 'https://meow.example.com/o'\''neil'"#)
        // A typed space is percent-encoded by URL parsing and needs no quoting.
        let space = try XCTUnwrap(MeowShared.serverURL(from: "https://meow.example.com/my meow"))
        XCTAssertEqual(AppModel.setupCommand(server: space), "meow setup --server https://meow.example.com/my%20meow")
        XCTAssertEqual(AppModel.shellQuoted("a b"), "'a b'")
        XCTAssertEqual(AppModel.shellQuoted("$(rm -rf ~)"), "'$(rm -rf ~)'")
        XCTAssertEqual(AppModel.shellQuoted(""), "''")
        XCTAssertFalse(AppModel.setupCommand(server: quote).contains("meow_"), "the Device Key never goes in the command")
    }

    func testParsesServerAddresses() {
        XCTAssertNil(MeowShared.serverURL(from: nil))
        XCTAssertNil(MeowShared.serverURL(from: ""))
        XCTAssertNil(MeowShared.serverURL(from: "   "))
        XCTAssertNil(MeowShared.serverURL(from: "localhost"))
        XCTAssertNil(MeowShared.serverURL(from: "meow.example.com"))
        XCTAssertNil(MeowShared.serverURL(from: "ftp://meow.example.com"))
        XCTAssertEqual(MeowShared.serverURL(from: "  https://meow.example.com/// \n")?.absoluteString, "https://meow.example.com")
        XCTAssertEqual(MeowShared.serverURL(from: "HTTPS://meow.example.com:8443/meow/?q=1#f")?.absoluteString,
                       "HTTPS://meow.example.com:8443/meow")
    }

    func testFreshInstallSuggestsOfficialServer() {
        // The official default is only a suggestion before a server is saved.
        XCTAssertEqual(MeowShared.defaultServer?.absoluteString, "https://api.meow.blue")
    }
}

extension SessionRegressionTests {
    func testSavedServerOverridesOfficialDefault() {
        let snapshot = Snapshot()
        defer { snapshot.restore() }
        MeowShared.server = serverA
        XCTAssertEqual(MeowShared.server, serverA, "an existing install keeps its server")
        XCTAssertEqual(AppModel().server, serverA)
        MeowShared.server = nil
        XCTAssertNil(MeowShared.server, "the default is only a UI suggestion, not a saved server")
    }

    func testHealthCheckExplainsWhatIsWrong() async {
        defer { ReviewURLProtocol.uninstall() }
        var reply = (200, Data(#"{"status":"ok"}"#.utf8))
        ReviewURLProtocol.install { req in
            XCTAssertEqual(req.url?.path, "/healthz")
            return reply
        }
        let ok = await AppModel.checkServer(" https://a.meow-review.invalid/ ")
        XCTAssertEqual(ok, .success(serverA))
        reply = (200, Data("<html>hello</html>".utf8))
        let html = await AppModel.checkServer("https://a.meow-review.invalid")
        XCTAssertEqual(html, .failure(.notMeow(host: "a.meow-review.invalid")))
        reply = (404, Data())
        let missing = await AppModel.checkServer("https://a.meow-review.invalid")
        XCTAssertEqual(missing, .failure(.notMeow(host: "a.meow-review.invalid")))
        reply = (503, Data(#"{"error":{"code":"unavailable","message":"database unavailable"}}"#.utf8))
        let down = await AppModel.checkServer("https://a.meow-review.invalid")
        XCTAssertEqual(down, .failure(.unreachable(host: "a.meow-review.invalid")))
        let typo = await AppModel.checkServer("meow.example.com")
        XCTAssertEqual(typo, .failure(.invalidAddress))
    }
}
