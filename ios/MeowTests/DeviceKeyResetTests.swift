import XCTest
@testable import Meow

private final class KeyResetProtocol: URLProtocol, @unchecked Sendable {
    static let lock = NSLock()
    static var handler: ((URLRequest) throws -> (Int, Data))?
    static func install(_ handler: @escaping (URLRequest) throws -> (Int, Data)) {
        lock.withLock { Self.handler = handler }
        URLProtocol.registerClass(Self.self)
    }
    static func uninstall() {
        URLProtocol.unregisterClass(Self.self)
        lock.withLock { handler = nil }
    }
    override class func canInit(with request: URLRequest) -> Bool { request.url?.host?.hasSuffix("key-reset.invalid") == true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        DispatchQueue.global().async {
            do {
                guard let handle = Self.lock.withLock({ Self.handler }) else { throw URLError(.cancelled) }
                let (status, data) = try handle(self.request)
                self.client?.urlProtocol(self, didReceive: HTTPURLResponse(url: self.request.url!, statusCode: status,
                    httpVersion: nil, headerFields: nil)!, cacheStoragePolicy: .notAllowed)
                self.client?.urlProtocol(self, didLoad: data)
                self.client?.urlProtocolDidFinishLoading(self)
            } catch { self.client?.urlProtocol(self, didFailWithError: error) }
        }
    }
    override func stopLoading() {}
}

private final class KeyResetRequests: @unchecked Sendable {
    private let lock = NSLock()
    private var requests: [URLRequest] = []
    @discardableResult
    func append(_ request: URLRequest) -> Int { lock.withLock { requests.append(request); return requests.count } }
    var values: [URLRequest] { lock.withLock { requests } }
}

@MainActor
final class DeviceKeyResetTests: XCTestCase {
    private let server = URL(string: "https://key-reset.invalid")!
    private let token = "mdt_reset_fixture"
    private var previousDefaults: [String: Any] = [:]
    private var previousToken: String?
    private var previousKey: String?
    private var folder: URL!
    private var store: InboxStore!

    override func setUp() async throws {
        previousDefaults = MeowShared.defaults.dictionaryRepresentation()
        previousToken = MeowShared.deviceToken
        previousKey = Keychain.get("device_key")
        MeowShared.server = server
        MeowShared.deviceToken = token
        MeowShared.deviceID = "dev_reset_fixture"
        Keychain.set("meow_old_fixture", for: "device_key")
        MeowShared.defaults.set("key_old", forKey: "device_key_id")
        MeowShared.defaults.removeObject(forKey: "key_pending_revocation")
        MeowShared.defaults.set(true, forKey: "onboarded")
        folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
    }

    override func tearDown() async throws {
        KeyResetProtocol.uninstall()
        for key in MeowShared.defaults.dictionaryRepresentation().keys { MeowShared.defaults.removeObject(forKey: key) }
        for (key, value) in previousDefaults { MeowShared.defaults.set(value, forKey: key) }
        MeowShared.deviceToken = previousToken
        Keychain.set(previousKey, for: "device_key")
        try? FileManager.default.removeItem(at: folder)
    }

    private func key(_ secret: String? = "meow_new_fixture") -> Data {
        var json: [String: Any] = ["id": "key_new", "name": "Default", "hint": "fixture", "scopes": ["notify", "ask"],
                                   "created_at": "2026-10-28T00:00:00Z"]
        if let secret { json["secret"] = secret }
        return try! JSONSerialization.data(withJSONObject: json)
    }

    func testSuccessReplacesOnlyTheSenderKeyAndPreservesHistory() async throws {
        let item = MeowNotification(id: "ntf_kept", type: .info, title: "Saved history", body: "Keep this message",
                                    source: "Agent", priority: "normal", pushStatus: "accepted", createdAt: .now)
        let scope = InboxStore.scope(server: server, token: token)
        try await store.save([item], scope: scope)
        let requests = KeyResetRequests()
        let response = key()
        KeyResetProtocol.install { request in
            requests.append(request)
            if request.httpMethod == "POST" { return (201, response) }
            XCTAssertEqual(Keychain.get("device_key"), "meow_new_fixture", "save the replacement before revoking")
            return (204, Data())
        }
        let model = AppModel(inboxStore: store)
        await model.rotateKey()
        XCTAssertEqual(model.deviceKey, "meow_new_fixture")
        XCTAssertEqual(Keychain.get("device_key"), "meow_new_fixture")
        XCTAssertEqual(MeowShared.defaults.string(forKey: "device_key_id"), "key_new")
        XCTAssertEqual(requests.values.map { $0.httpMethod! + " " + $0.url!.path }, ["POST /v1/keys", "DELETE /v1/keys/key_old"])
        XCTAssertTrue(requests.values.allSatisfy { $0.value(forHTTPHeaderField: "Authorization") == "Bearer " + token })
        let history = try await store.load(scope: scope)
        XCTAssertEqual(history?.map(\.id), [item.id])
        XCTAssertEqual(MeowShared.deviceToken, token)
        XCTAssertEqual(MeowShared.deviceID, "dev_reset_fixture")
        XCTAssertTrue(model.isRegistered)
        XCTAssertTrue(model.hasFinishedOnboarding)
        XCTAssertNil(model.alert)
        XCTAssertNotNil(model.toast)
    }

    func testCreationFailureKeepsCurrentKey() async {
        KeyResetProtocol.install { request in
            XCTAssertEqual(request.httpMethod, "POST")
            throw URLError(.notConnectedToInternet)
        }
        let model = AppModel(inboxStore: store)
        await model.rotateKey()
        XCTAssertEqual(model.deviceKey, "meow_old_fixture")
        XCTAssertEqual(MeowShared.defaults.string(forKey: "device_key_id"), "key_old")
        XCTAssertNotNil(model.alert)
        XCTAssertNil(model.toast)
    }

    func testMissingOrEmptySecretNeverRevokesCurrentKey() async {
        for secret: String? in [nil, "", "   "] {
            let requests = KeyResetRequests()
            let response = key(secret)
            KeyResetProtocol.install { request in
                requests.append(request)
                return (201, response)
            }
            let model = AppModel(inboxStore: store)
            await model.rotateKey()
            XCTAssertEqual(requests.values.count, 1)
            XCTAssertEqual(model.deviceKey, "meow_old_fixture")
            XCTAssertEqual(Keychain.get("device_key"), "meow_old_fixture")
            XCTAssertEqual(MeowShared.defaults.string(forKey: "device_key_id"), "key_old")
            XCTAssertNotNil(model.alert)
            XCTAssertNil(model.toast)
        }
    }

    func testFailedRevocationRetriesAcrossModelRestartWithoutCreatingAnotherKey() async {
        let requests = KeyResetRequests()
        let response = key()
        KeyResetProtocol.install { request in
            requests.append(request)
            if request.httpMethod == "POST" { return (201, response) }
            return (503, Data(#"{"error":{"code":"unavailable","message":"Try again"}}"#.utf8))
        }
        let model = AppModel(inboxStore: store)
        await model.rotateKey()
        XCTAssertEqual(model.deviceKey, "meow_new_fixture")
        XCTAssertNotNil(model.alert)
        XCTAssertNil(model.toast)
        KeyResetProtocol.install { request in
            requests.append(request)
            if request.httpMethod == "POST" { return (201, response) }
            return (204, Data())
        }
        let restarted = AppModel(inboxStore: store)
        await restarted.rotateKey()
        XCTAssertEqual(requests.values.filter { $0.httpMethod == "POST" }.count, 1)
        XCTAssertEqual(requests.values.filter { $0.httpMethod == "DELETE" }.map { $0.url!.lastPathComponent }, ["key_old", "key_old"])
        XCTAssertNil(restarted.alert)
        XCTAssertNotNil(restarted.toast)
        XCTAssertNil(MeowShared.defaults.dictionary(forKey: "key_pending_revocation"))
    }

    func testConcurrentResetCreatesOnlyOneKey() async {
        let started = expectation(description: "creation started")
        let release = DispatchSemaphore(value: 0)
        let requests = KeyResetRequests()
        let response = key()
        KeyResetProtocol.install { request in
            let count = requests.append(request)
            if request.httpMethod == "POST" {
                if count == 1 { started.fulfill(); _ = release.wait(timeout: .now() + 5) }
                return (201, response)
            }
            return (204, Data())
        }
        let model = AppModel(inboxStore: store)
        let first = Task { await model.rotateKey() }
        await fulfillment(of: [started], timeout: 3)
        await model.rotateKey()
        release.signal()
        await first.value
        XCTAssertEqual(requests.values.filter { $0.httpMethod == "POST" }.count, 1)
    }

    func testKeychainFailureKeepsOldKeyAndOnlyCleansUpReplacement() async {
        for cleanupFails in [false, true] {
            let requests = KeyResetRequests()
            let response = key()
            KeyResetProtocol.install { request in
                requests.append(request)
                if request.httpMethod == "DELETE" {
                    XCTAssertEqual(request.url!.lastPathComponent, "key_new")
                    if cleanupFails { throw URLError(.notConnectedToInternet) }
                    return (204, Data())
                }
                return (201, response)
            }
            let model = AppModel(inboxStore: store, saveDeviceKey: { _ in false })
            await model.rotateKey()
            XCTAssertEqual(model.deviceKey, "meow_old_fixture")
            XCTAssertEqual(Keychain.get("device_key"), "meow_old_fixture")
            XCTAssertEqual(MeowShared.defaults.string(forKey: "device_key_id"), "key_old")
            XCTAssertNil(MeowShared.defaults.dictionary(forKey: "key_pending_revocation"))
            XCTAssertEqual(requests.values.count, 2)
            XCTAssertNotNil(model.alert)
            XCTAssertFalse(model.alert?.contains("meow_") == true)
            XCTAssertNil(model.toast)
        }
    }

    func testAlreadyAbsentOldKeyCompletesReset() async {
        let response = key()
        KeyResetProtocol.install { request in
            if request.httpMethod == "POST" { return (201, response) }
            return (404, Data(#"{"error":{"code":"not_found","message":"Gone"}}"#.utf8))
        }
        let model = AppModel(inboxStore: store)
        await model.rotateKey()
        XCTAssertEqual(model.deviceKey, "meow_new_fixture")
        XCTAssertNil(model.alert)
        XCTAssertNotNil(model.toast)
        XCTAssertNil(MeowShared.defaults.dictionary(forKey: "key_pending_revocation"))
    }

    func testForeignPendingRevocationIsNeverUsed() async {
        for (otherServer, otherToken) in [(URL(string: "https://other.key-reset.invalid")!, token), (server, "mdt_other")] {
            let foreign = InboxStore.scope(server: otherServer, token: otherToken)
            MeowShared.defaults.set(["scope": foreign, "id": "key_foreign"], forKey: "key_pending_revocation")
            MeowShared.defaults.set("key_old", forKey: "device_key_id")
            Keychain.set("meow_old_fixture", for: "device_key")
            let requests = KeyResetRequests()
            let response = key()
            KeyResetProtocol.install { request in
                requests.append(request)
                return request.httpMethod == "POST" ? (201, response) : (204, Data())
            }
            let model = AppModel(inboxStore: store)
            await model.rotateKey()
            XCTAssertEqual(requests.values.map { $0.url!.path }, ["/v1/keys", "/v1/keys/key_old"])
            XCTAssertNil(model.alert)
        }
    }

    func testMissingOldKeyIDDoesNotCreateAnUnrevokableReplacement() async {
        MeowShared.defaults.removeObject(forKey: "device_key_id")
        KeyResetProtocol.install { _ in XCTFail("cannot revoke an unidentified old key"); throw URLError(.badURL) }
        let model = AppModel(inboxStore: store)
        await model.rotateKey()
        XCTAssertEqual(model.deviceKey, "meow_old_fixture")
        XCTAssertNotNil(model.alert)
        XCTAssertNil(model.toast)
    }

    func testUnregisteredDeviceCannotRotate() async {
        MeowShared.deviceToken = nil
        KeyResetProtocol.install { _ in XCTFail("unregistered device sent a key request"); throw URLError(.badURL) }
        let model = AppModel(inboxStore: store)
        await model.rotateKey()
        XCTAssertEqual(model.deviceKey, "meow_old_fixture")
    }

    func testLateCreationAndRevocationFailureCannotAffectNewRegistration() async throws {
        for holdRevocation in [false, true] {
            MeowShared.server = server; MeowShared.deviceToken = token
            Keychain.set("meow_old_fixture", for: "device_key")
            MeowShared.defaults.set("key_old", forKey: "device_key_id")
            MeowShared.defaults.removeObject(forKey: "key_pending_revocation")
            let started = expectation(description: "old operation paused")
            let release = DispatchSemaphore(value: 0)
            let response = key()
            let next = URL(string: "https://next.key-reset.invalid")!
            let requests = KeyResetRequests()
            KeyResetProtocol.install { request in
                requests.append(request)
                if request.url!.path == "/v1/devices" {
                    return (201, Data(#"{"device":{"id":"dev_next","name":"Fixture","apns_env":"sandbox","has_apns_token":false,"created_at":"2026-10-28T00:00:00Z","last_seen_at":"2026-10-28T00:00:00Z"},"device_token":"mdt_next","key":{"id":"key_next","name":"Default","hint":"fixture","scopes":["notify"],"created_at":"2026-10-28T00:00:00Z","secret":"meow_next_fixture"}}"#.utf8))
                }
                if request.httpMethod == (holdRevocation ? "DELETE" : "POST") {
                    started.fulfill(); _ = release.wait(timeout: .now() + 8)
                }
                if request.httpMethod == "DELETE" { throw URLError(.notConnectedToInternet) }
                return (201, response)
            }
            let queue = ReplyQueue(fileURL: folder.appendingPathComponent("queue.json")) { _ in throw URLError(.badURL) }
            let model = AppModel(replies: queue, inboxStore: store)
            let rotation = Task { await model.rotateKey() }
            await fulfillment(of: [started], timeout: 3)
            let switched = await model.reset(server: next)
            XCTAssertTrue(switched)
            await model.register()
            release.signal()
            await rotation.value
            XCTAssertEqual(model.deviceKey, "meow_next_fixture")
            XCTAssertEqual(Keychain.get("device_key"), "meow_next_fixture")
            XCTAssertEqual(MeowShared.deviceToken, "mdt_next")
            XCTAssertEqual(MeowShared.defaults.string(forKey: "device_key_id"), "key_next")
            XCTAssertNil(model.alert)
            XCTAssertNil(model.toast)
            XCTAssertFalse(requests.values.contains { $0.url!.host == next.host && $0.httpMethod == "DELETE" })
        }
    }
}
