import Foundation

/// A non-2xx response from the Meow API.
struct MeowAPIError: LocalizedError, Sendable {
    var status: Int
    var code: String
    var message: String
    /// The question's current state, sent with 409 already_resolved and 410 expired.
    var question: MeowQuestion?

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
            throw MeowAPIError(status: http.statusCode, code: e?.error?.code ?? "",
                               message: e?.error?.message ?? "", question: e?.question)
        }
        if T.self == Empty.self, data.isEmpty, let empty = Empty() as? T {
            return empty
        }
        return try MeowJSON.decoder.decode(T.self, from: data)
    }

    private func sendEmpty(_ method: String, _ path: String, timeout: TimeInterval = 30) async throws {
        let _: Empty = try await send(method, path, timeout: timeout)
    }

    // MARK: Device

    /// Registers an anonymous device. Needs no token.
    func register(name: String) async throws -> MeowRegistration {
        try await send("POST", "/v1/devices", body: ["name": name])
    }

    func updateDevice(apnsToken: String, apnsEnv: String) async throws -> MeowDevice {
        try await send("PATCH", "/v1/device", body: ["apns_token": apnsToken, "apns_env": apnsEnv])
    }

    // MARK: Inbox

    func inbox(filter: String, search: String, cursor: String?, limit: Int = 50) async throws -> InboxPage {
        var q = [URLQueryItem(name: "filter", value: filter), URLQueryItem(name: "limit", value: String(limit))]
        if !search.isEmpty { q.append(URLQueryItem(name: "q", value: search)) }
        if let cursor { q.append(URLQueryItem(name: "cursor", value: cursor)) }
        return try await send("GET", "/v1/inbox", query: q)
    }

    func counts() async throws -> InboxCounts {
        try await send("GET", "/v1/inbox/counts")
    }

    func notification(_ id: String) async throws -> MeowNotification {
        try await send("GET", "/v1/notifications/\(id)")
    }

    func markRead(_ id: String) async throws {
        try await sendEmpty("POST", "/v1/notifications/\(id)/read")
    }

    func markAllRead() async throws {
        try await sendEmpty("POST", "/v1/inbox/read")
    }

    func markDelivered(_ id: String) async throws {
        try await sendEmpty("POST", "/v1/notifications/\(id)/delivered", timeout: 10)
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

    func notify(title: String, body: String) async throws -> MeowNotification {
        try await send("POST", "/v1/notifications", body: ["title": title, "body": body])
    }
}
