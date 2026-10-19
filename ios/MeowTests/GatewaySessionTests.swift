import XCTest
@testable import Meow

private final class GatewayURLProtocol: URLProtocol, @unchecked Sendable {
    static let lock = NSLock()
    static var callback: ((URLRequest) throws -> (Int, Data))?
    static func session(_ callback: @escaping (URLRequest) throws -> (Int, Data)) -> URLSession {
        lock.withLock { Self.callback = callback }
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [Self.self]
        return URLSession(configuration: config)
    }
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        do {
            guard let callback = Self.lock.withLock({ Self.callback }) else { throw URLError(.cancelled) }
            let (status, body) = try callback(request)
            let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil,
                                           headerFields: ["Content-Type": "application/json"])!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: body)
            client?.urlProtocolDidFinishLoading(self)
        } catch { client?.urlProtocol(self, didFailWithError: error) }
    }
    override func stopLoading() {}
}

@MainActor
final class GatewaySessionTests: XCTestCase {
    private struct Snapshot {
        let server = MeowShared.server
        let token = MeowShared.deviceToken
        let deviceID = MeowShared.deviceID
        let binding = MeowShared.gatewayBinding
        let onboarded = MeowShared.defaults.bool(forKey: "onboarded")
        let key = Keychain.get("device_key")
        let keyID = MeowShared.defaults.string(forKey: "device_key_id")
        func restore() {
            MeowShared.server = server; MeowShared.deviceToken = token; MeowShared.deviceID = deviceID
            MeowShared.saveGatewayBinding(binding)
            MeowShared.defaults.set(onboarded, forKey: "onboarded")
            Keychain.set(key, for: "device_key")
            MeowShared.defaults.set(keyID, forKey: "device_key_id")
        }
    }
    private let server = URL(string: "https://origin.gateway-test.invalid")!
    private func prepare() {
        MeowShared.server = server
        MeowShared.deviceToken = "mdt_mobile"
        MeowShared.deviceID = "dev_test"
        MeowShared.saveGatewayBinding(nil)
    }
    private func json(_ value: Any) throws -> Data { try JSONSerialization.data(withJSONObject: value) }

    private func enrollment(_ req: URLRequest) throws -> (Int, Data) {
        let data: Data
        if let body = req.httpBody { data = body }
        else if let stream = req.httpBodyStream {
            stream.open(); defer { stream.close() }
            var collected = Data(); var buffer = [UInt8](repeating: 0, count: 4096)
            while stream.hasBytesAvailable {
                let count = stream.read(&buffer, maxLength: buffer.count)
                if count < 0 { throw stream.streamError ?? URLError(.cannotDecodeRawData) }
                if count == 0 { break }
                collected.append(buffer, count: count)
            }
            data = collected
        } else { throw URLError(.badServerResponse) }
        let input = try JSONSerialization.jsonObject(with: data) as! [String: String]
        return (201, try json(["id": "gwd_device", "management_token": "gwm_owner", "push_token": "gwp_" + String(repeating: "a", count: 39),
                               "binding_id": input["binding_id"]!]))
    }

    func testGatewayRegistersAutomaticallyWithoutExtraCredentials() async throws {
        let snapshot = Snapshot(); defer { snapshot.restore() }
        prepare()
        let session = GatewayURLProtocol.session { req in
            if req.url!.path == "/v1/push-config" {
                XCTAssertEqual(req.value(forHTTPHeaderField: "Authorization"), "Bearer mdt_mobile")
                return (200, try self.json(["mode": "gateway", "gateway_url": "https://push.gateway-test.invalid"]))
            }
            if req.url!.path == "/v1/gateway/devices" {
                XCTAssertNil(req.value(forHTTPHeaderField: "Authorization"))
                return try self.enrollment(req)
            }
            XCTAssertEqual(req.url!.path, "/v1/device/push-gateway")
            return (204, Data())
        }
        defer { session.invalidateAndCancel() }
        let model = AppModel(pushSession: session)
        model.didReceiveAPNsToken(String(repeating: "a", count: 64))
        await model.configurePush()
        XCTAssertTrue(model.hasPushToken)
        XCTAssertNil(model.pushRegistrationError)
        XCTAssertNotNil(MeowShared.gatewayBinding)
        XCTAssertEqual(MeowShared.deviceToken, "mdt_mobile")
    }

    func testBindingRetryReusesEnrollmentAndSeparatesCredentials() async throws {
        let snapshot = Snapshot(); defer { snapshot.restore() }
        prepare()
        var enrollments = 0
        var bindings = 0
        let session = GatewayURLProtocol.session { req in
            let path = req.url!.path
            switch path {
            case "/v1/push-config":
                return (200, try self.json(["mode": "gateway", "gateway_url": "https://push.gateway-test.invalid"]))
            case "/v1/gateway/devices":
                XCTAssertNil(req.value(forHTTPHeaderField: "Authorization"))
                enrollments += 1
                return try self.enrollment(req)
            case "/v1/gateway/devices/gwd_device":
                XCTAssertEqual(req.value(forHTTPHeaderField: "Authorization"), "Bearer gwm_owner")
                return (204, Data())
            case "/v1/device/push-gateway":
                XCTAssertEqual(req.value(forHTTPHeaderField: "Authorization"), "Bearer mdt_mobile")
                bindings += 1
                return (bindings == 1 ? 503 : 204, Data())
            default: throw URLError(.badURL)
            }
        }
        defer { session.invalidateAndCancel() }
        let model = AppModel(pushSession: session)
        model.didReceiveAPNsToken(String(repeating: "a", count: 64))
        await model.configurePush()
        XCTAssertFalse(model.hasPushToken)
        XCTAssertNotNil(MeowShared.gatewayBinding, "a transient API failure must not lose the durable enrollment")
        await model.configurePush()
        XCTAssertTrue(model.hasPushToken)
        XCTAssertEqual(enrollments, 1)
        XCTAssertEqual(bindings, 2)
    }
}

extension GatewaySessionTests {
    func testLateEnrollmentIsRevokedAfterChangingServers() async throws {
        let snapshot = Snapshot(); defer { snapshot.restore() }
        prepare()
        let started = expectation(description: "enrollment started")
        let revoked = expectation(description: "late enrollment revoked")
        let gate = DispatchSemaphore(value: 0)
        let session = GatewayURLProtocol.session { req in
            switch req.url!.path {
            case "/v1/push-config":
                return (200, try self.json(["mode": "gateway", "gateway_url": "https://push.gateway-test.invalid"]))
            case "/v1/gateway/devices":
                started.fulfill()
                _ = gate.wait(timeout: .now() + 10)
                return try self.enrollment(req)
            case "/v1/gateway/devices/gwd_device":
                XCTAssertEqual(req.httpMethod, "DELETE")
                XCTAssertEqual(req.value(forHTTPHeaderField: "Authorization"), "Bearer gwm_owner")
                revoked.fulfill()
                return (204, Data())
            default:
                XCTFail("a late enrollment must not be bound to either server")
                throw URLError(.badURL)
            }
        }
        defer { session.invalidateAndCancel() }
        let file = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: file) }
        let replies = ReplyQueue(fileURL: file) { _ in throw URLError(.notConnectedToInternet) }
        let model = AppModel(replies: replies, pushSession: session)
        let enrolling = Task {
            model.didReceiveAPNsToken(String(repeating: "a", count: 64))
            await model.configurePush()
        }
        await fulfillment(of: [started], timeout: 3)
        let next = URL(string: "https://next.gateway-test.invalid")!
        let switched = await model.reset(server: next)
        XCTAssertTrue(switched)
        gate.signal()
        await enrolling.value
        await fulfillment(of: [revoked], timeout: 3)
        XCTAssertEqual(model.server, next)
        XCTAssertFalse(model.isRegistered)
        XCTAssertFalse(model.hasPushToken)
        XCTAssertNil(MeowShared.gatewayBinding)
        XCTAssertNil(MeowShared.deviceToken)
    }
}

extension GatewaySessionTests {
    func testLegacyServer404KeepsDirectPushAndLearnsDeviceID() async throws {
        let snapshot = Snapshot(); defer { snapshot.restore() }
        prepare()
        MeowShared.deviceID = nil // installed before device IDs were saved locally
        let session = GatewayURLProtocol.session { req in
            XCTAssertEqual(req.value(forHTTPHeaderField: "Authorization"), "Bearer mdt_mobile")
            if req.url!.path == "/v1/push-config" { return (404, Data()) }
            XCTAssertEqual(req.url!.path, "/v1/device")
            XCTAssertEqual(req.httpMethod, "PATCH")
            return (200, try self.json(["id": "dev_legacy", "name": "phone", "apns_env": "sandbox", "has_apns_token": true,
                                       "created_at": "2026-10-17T00:00:00Z", "last_seen_at": "2026-10-17T00:00:00Z"]))
        }
        defer { session.invalidateAndCancel() }
        let model = AppModel(pushSession: session)
        model.didReceiveAPNsToken(String(repeating: "a", count: 64))
        await model.configurePush()
        XCTAssertTrue(model.hasPushToken)
        XCTAssertNil(model.gatewayURL)
        XCTAssertEqual(MeowShared.deviceID, "dev_legacy")
        XCTAssertTrue(NotificationIdentity.current!.accepts(["id": "ntf_1", "device_id": "dev_legacy"]))
    }
}

extension GatewaySessionTests {
    func testMissingServerReturnsToOnboarding() {
        let snapshot = Snapshot(); defer { snapshot.restore() }
        prepare()
        MeowShared.server = nil
        XCTAssertFalse(AppModel().isRegistered, "a saved token without a server cannot form an API session")
    }
}
