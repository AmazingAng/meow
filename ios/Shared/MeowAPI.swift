import Foundation

/// A non-2xx response from the Meow API.
struct MeowAPIError: LocalizedError, Sendable {
    var status: Int
    var code: String
    var message: String
    /// The question's current state, sent with 409 already_resolved and 410 expired.
    var question: MeowQuestion?
    var retryAfter: Duration?

    var errorDescription: String? { message.isEmpty ? "HTTP \(status)" : message }

    /// Retrying the same request may succeed.
    var isTemporary: Bool { status == 429 || status >= 500 }
}

/// Client for the Meow HTTP API. `token` is the device token (mdt_…) for the
/// mobile endpoints, or the Device Key (meow_…) for sending.
struct MeowAPI: Sendable {
    var server: URL
    var token: String?
    var session: URLSession = .shared

    private struct Empty: Decodable {}

    private struct ErrorBody: Decodable {
        struct Detail: Decodable {
            var code: String
            var message: String
        }
        var error: Detail?
        var question: MeowQuestion?
    }

    private func send<T: Decodable>(
        _ method: String,
        _ path: String,
        query: [URLQueryItem] = [],
        body: (any Encodable)? = nil,
        idempotencyKey: String? = nil,
        timeout: TimeInterval = 30
    ) async throws -> T {
        guard var comps = URLComponents(url: server, resolvingAgainstBaseURL: false) else {
            throw URLError(.badURL)
        }
        let base = comps.path.hasSuffix("/") ? String(comps.path.dropLast()) : comps.path
        comps.path = base + path
        comps.queryItems = query.isEmpty ? nil : query
        guard let url = comps.url else { throw URLError(.badURL) }

        var req = URLRequest(url: url, timeoutInterval: timeout)
        req.httpMethod = method
        if let token { req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        if let idempotencyKey { req.setValue(idempotencyKey, forHTTPHeaderField: "Idempotency-Key") }
        if let body {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = try MeowJSON.encoder.encode(body)
        }

        let (data, response) = try await session.data(for: req)
        guard let http = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        guard (200..<300).contains(http.statusCode) else {
            let e = try? MeowJSON.decoder.decode(ErrorBody.self, from: data)
            let seconds = http.value(forHTTPHeaderField: "Retry-After")
                .flatMap { Int64($0.trimmingCharacters(in: .whitespacesAndNewlines)) }
            let delay = seconds.flatMap { $0 >= 0 ? Duration.seconds($0) : nil }
            throw MeowAPIError(status: http.statusCode, code: e?.error?.code ?? "",
                               message: e?.error?.message ?? "", question: e?.question, retryAfter: delay)
        }
        if T.self == Empty.self, data.isEmpty, let empty = Empty() as? T {
            return empty
        }
        return try MeowJSON.decoder.decode(T.self, from: data)
    }

    private func sendEmpty(_ method: String, _ path: String, timeout: TimeInterval = 30) async throws {
        let _: Empty = try await send(method, path, timeout: timeout)
    }

    // MARK: Server

    /// Whether the server answers GET /healthz the way a Meow server does.
    func isHealthy() async throws -> Bool {
        struct Health: Decodable { var status: String }
        let health: Health = try await send("GET", "/healthz", timeout: 10)
        return health.status == "ok"
    }

    // MARK: Device

    /// Registers an anonymous device. Needs no token.
    func register(name: String) async throws -> MeowRegistration {
        try await send("POST", "/v1/devices", body: ["name": name])
    }

    func device() async throws -> MeowDevice { try await send("GET", "/v1/device") }

    func updateDevice(apnsToken: String, apnsEnv: String) async throws -> MeowDevice {
        try await send("PATCH", "/v1/device", body: ["apns_token": apnsToken, "apns_env": apnsEnv])
    }

    // MARK: Wake gateway

    func pushConfiguration() async throws -> PushConfiguration {
        do { return try await send("GET", "/v1/push-config") }
        catch let e as MeowAPIError where e.status == 404 {
            return PushConfiguration(mode: "direct") // compatible with pre-gateway servers
        }
    }

    func bindGateway(_ binding: GatewayBinding) async throws {
        let _: Empty = try await send("PUT", "/v1/device/push-gateway", body: [
            "gateway_device_id": binding.routeID, "push_token": binding.pushToken, "binding_id": binding.bindingID])
    }

    func enrollGateway(apnsToken: String, environment: String, bindingID: String) async throws -> GatewayRegistration {
        try await send("POST", "/v1/gateway/devices", body: [
            "apns_token": apnsToken, "apns_env": environment, "binding_id": bindingID])
    }

    func updateGateway(_ routeID: String, apnsToken: String, environment: String) async throws {
        let _: Empty = try await send("PATCH", "/v1/gateway/devices/\(routeID)", body: [
            "apns_token": apnsToken, "apns_env": environment])
    }

    func revokeGateway(_ routeID: String) async throws {
        try await sendEmpty("DELETE", "/v1/gateway/devices/\(routeID)")
    }

    // MARK: Inbox

    func deleteNotification(_ id: String) async throws {
        try await sendEmpty("DELETE", "/v1/notifications/\(id)")
    }

    func deleteNotifications(in interval: InboxDeletionInterval) async throws {
        struct Result: Decodable { let deleted: Int }
        let _: Result = try await send("DELETE", "/v1/inbox", query: [
            URLQueryItem(name: "from", value: String(interval.from)),
            URLQueryItem(name: "before", value: String(interval.before))])
    }

    func inbox(filter: String, search: String, cursor: String?, limit: Int = 50) async throws -> InboxPage {
        var q = [URLQueryItem(name: "filter", value: filter), URLQueryItem(name: "limit", value: String(limit))]
        if !search.isEmpty { q.append(URLQueryItem(name: "q", value: search)) }
        if let cursor { q.append(URLQueryItem(name: "cursor", value: cursor)) }
        return try await send("GET", "/v1/inbox", query: q)
    }

    /// Read the complete pending set before sorting by urgency. Ordinary inbox
    /// pagination is chronological and can hide an older, more urgent question.
    func pendingInbox(search: String = "", firstPage: InboxPage? = nil) async throws -> [MeowNotification] {
        var page: InboxPage
        if let firstPage { page = firstPage }
        else { page = try await inbox(filter: "pending", search: search, cursor: nil, limit: 200) }
        var result = page.notifications
        var seenIDs = Set(result.map(\.id))
        var cursors = Set<String>()
        while let cursor = page.nextCursor {
            try Task.checkCancellation()
            guard cursors.insert(cursor).inserted else { throw URLError(.badServerResponse) }
            page = try await inbox(filter: "pending", search: search, cursor: cursor, limit: 200)
            result += page.notifications.filter { seenIDs.insert($0.id).inserted }
        }
        return result
    }

    /// Synchronize history independently of the current local display query.
    /// A failed page throws without returning a misleading partial snapshot.
    func allInbox() async throws -> [MeowNotification] {
        var cursor: String?
        var cursors = Set<String>()
        var seen = Set<String>()
        var items: [MeowNotification] = []
        repeat {
            try Task.checkCancellation()
            let page = try await historyPage(cursor: cursor)
            items += page.notifications.filter { seen.insert($0.id).inserted }
            cursor = page.nextCursor
            if let cursor, !cursors.insert(cursor).inserted { throw URLError(.badServerResponse) }
        } while cursor != nil
        return items
    }

    private func historyPage(cursor: String?) async throws -> InboxPage {
        var retries = 0
        while true {
            do { return try await inbox(filter: "all", search: "", cursor: cursor, limit: 200) }
            catch let error as MeowAPIError where error.status == 429 && retries < 3 {
                retries += 1
                try await Task.sleep(for: error.retryAfter ?? .seconds(1))
            }
        }
    }

    func counts(timeout: TimeInterval = 30) async throws -> InboxCounts {
        try await send("GET", "/v1/inbox/counts", timeout: timeout)
    }

    func notification(_ id: String, timeout: TimeInterval = 30) async throws -> MeowNotification {
        try await send("GET", "/v1/notifications/\(id)", timeout: timeout)
    }

    func markRead(_ id: String) async throws {
        try await sendEmpty("POST", "/v1/notifications/\(id)/read")
    }

    func markAllRead() async throws {
        try await sendEmpty("POST", "/v1/inbox/read")
    }

    func markDelivered(_ id: String, timeout: TimeInterval = 10) async throws {
        try await sendEmpty("POST", "/v1/notifications/\(id)/delivered", timeout: timeout)
    }

    // MARK: Questions

    /// Answers a question. Reuse the same idempotency key when retrying.
    func respond(questionID: String, answer: String, idempotencyKey: String) async throws -> MeowQuestion {
        try await send("POST", "/v1/questions/\(questionID)/responses", body: ["answer": answer], idempotencyKey: idempotencyKey)
    }

    // MARK: Keys

    private struct NewKey: Encodable {
        var name: String
        var scopes: [String]
    }

    func createKey(name: String, scopes: [String]) async throws -> MeowKey {
        try await send("POST", "/v1/keys", body: NewKey(name: name, scopes: scopes))
    }

    func revokeKey(_ id: String) async throws {
        try await sendEmpty("DELETE", "/v1/keys/\(id)")
    }

    // MARK: Sending (Device Key)

    func notify(title: String, body: String, priority: String = "normal") async throws -> MeowNotification {
        try await send("POST", "/v1/notifications", body: ["title": title, "body": body, "priority": priority])
    }
}
