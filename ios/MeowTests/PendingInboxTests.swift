import XCTest
@testable import Meow

private final class PendingInboxProtocol: URLProtocol, @unchecked Sendable {
    static var handler: ((URLRequest) throws -> Data)?
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        do {
            let data = try Self.handler!(request)
            client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: 200,
                httpVersion: nil, headerFields: nil)!, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        } catch { client?.urlProtocol(self, didFailWithError: error) }
    }
    override func stopLoading() {}
}

final class PendingInboxTests: XCTestCase {
    private func api(_ handler: @escaping (URLRequest) throws -> Data) -> MeowAPI {
        PendingInboxProtocol.handler = handler
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [PendingInboxProtocol.self]
        let session = URLSession(configuration: config)
        addTeardownBlock { session.invalidateAndCancel(); PendingInboxProtocol.handler = nil }
        return MeowAPI(server: URL(string: "https://pending.invalid")!, token: "mdt_fixture", session: session)
    }

    func testCollectsPendingPagesBeyondTwoHundredAndPreservesSearch() async throws {
        let now = Date.now
        func item(_ id: String, expiry: TimeInterval) -> MeowNotification {
            MeowNotification(id: id, type: .question, title: id, body: "", source: "Codex",
                priority: "normal", pushStatus: "accepted", createdAt: now,
                question: .init(id: "q_" + id, status: .pending, expiresAt: now.addingTimeInterval(expiry)))
        }
        let first = (0..<200).map { item("ntf_\($0)", expiry: 3600) }
        let urgent = item("ntf_urgent", expiry: 120)
        let api = api { request in
            let query = URLComponents(url: request.url!, resolvingAgainstBaseURL: false)!.queryItems!
            XCTAssertTrue(query.contains { $0.name == "q" && $0.value == "project" })
            XCTAssertTrue(query.contains { $0.name == "filter" && $0.value == "pending" })
            let more = query.contains { $0.name == "cursor" && $0.value == "older" }
            let encoder = JSONEncoder()
            encoder.keyEncodingStrategy = .convertToSnakeCase
            encoder.dateEncodingStrategy = .iso8601
            return try encoder.encode(InboxPage(notifications: more ? [first[199], urgent] : first,
                                               nextCursor: more ? nil : "older"))
        }
        let items = try await api.pendingInbox(search: "project")
        XCTAssertEqual(items.count, 201, "overlapping pages must not duplicate a row")
        XCTAssertEqual(InboxPresentation(items, now: now).pending.first?.id, urgent.id)
    }

    func testPendingPageFailureDoesNotReturnMisleadingPartialSuccess() async throws {
        let api = api { request in
            if request.url!.query!.contains("cursor") { throw URLError(.notConnectedToInternet) }
            return try MeowJSON.encoder.encode(InboxPage(notifications: [], nextCursor: "next"))
        }
        do {
            _ = try await api.pendingInbox()
            XCTFail("the incomplete pending set must not be presented as a successful refresh")
        } catch { XCTAssertEqual((error as? URLError)?.code, .notConnectedToInternet) }
    }
}
