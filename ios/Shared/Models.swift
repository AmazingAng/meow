import Foundation

enum NotificationEvent: String, Codable, CaseIterable, Sendable {
    case result, attention, failure
}

enum QuestionStatus: String, Codable, Sendable {
    case pending, answered, rejected, expired, cancelled
}

struct MeowNotification: Codable, Identifiable, Hashable, Sendable {
    enum Kind: String, Codable, Sendable {
        case info, alert, question
    }

    struct Question: Codable, Hashable, Sendable {
        var id: String
        var status: QuestionStatus
        var choices: [String]?
        var answer: String?
        var expiresAt: Date
    }

    var id: String
    var type: Kind
    var title: String
    var body: String
    var source: String
    var priority: String
    var pushStatus: String
    var pushError: String?
    var createdAt: Date
    var deliveredAt: Date?
    var readAt: Date?
    var question: Question?
    var context: NotificationOrigin? = nil
    var event: NotificationEvent? = nil

    var isUnread: Bool { readAt == nil }
}

extension MeowNotification.Question {
    /// Still waiting for an answer: pending and not past its expiry.
    func isOpen(at now: Date = .now) -> Bool {
        status == .pending && expiresAt > now
    }

    /// Exactly the choices approve and reject, in any order or case.
    var isApproval: Bool {
        guard let choices, choices.count == 2 else { return false }
        return Set(choices.map { $0.lowercased() }) == ["approve", "reject"]
    }

    mutating func apply(_ q: MeowQuestion) {
        status = q.status
        answer = q.answer
        expiresAt = q.expiresAt
    }
}

/// A question as returned by the questions endpoints.
struct MeowQuestion: Codable, Hashable, Sendable {
    var id: String
    var status: QuestionStatus
    var question: String
    var choices: [String]?
    var answer: String?
    var createdAt: Date
    var expiresAt: Date
    var resolvedAt: Date?
    var notificationId: String
}

struct MeowKey: Codable, Hashable, Sendable {
    var id: String
    var name: String
    var hint: String
    var scopes: [String]
    var createdAt: Date
    var lastUsedAt: Date?
    var secret: String? // only present in the response that creates the key
}

struct MeowDevice: Codable, Hashable, Sendable {
    var id: String
    var name: String
    var apnsEnv: String
    var hasApnsToken: Bool
    var createdAt: Date
    var lastSeenAt: Date
}

struct MeowRegistration: Codable, Sendable {
    var device: MeowDevice
    var deviceToken: String
    var key: MeowKey
}

struct InboxCounts: Codable, Hashable, Sendable {
    var unread = 0
    var pending = 0
}

struct InboxPage: Codable, Sendable {
    var notifications: [MeowNotification]
    var nextCursor: String?
}

enum MeowJSON {
    static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        d.dateDecodingStrategy = .custom { decoder in
            let c = try decoder.singleValueContainer()
            let s = try c.decode(String.self)
            guard let date = parseDate(s) else {
                throw DecodingError.dataCorruptedError(in: c, debugDescription: "invalid RFC 3339 date \(s)")
            }
            return date
        }
        return d
    }()

    static let encoder = JSONEncoder()

    nonisolated(unsafe) private static let formatter = ISO8601DateFormatter() // thread-safe

    /// Parses RFC 3339 timestamps with any number of fractional digits, as Go
    /// emits them ("2026-10-17T06:28:11.12Z"). ISO8601DateFormatter only
    /// accepts exactly three, so the fraction is handled separately.
    static func parseDate(_ s: String) -> Date? {
        var base = Substring(s)
        var fraction = 0.0
        if let dot = s.firstIndex(of: "."),
           let end = s[dot...].firstIndex(where: { $0 == "Z" || $0 == "z" || $0 == "+" || $0 == "-" }) {
            fraction = Double("0" + s[dot..<end]) ?? 0
            base = s[..<dot] + s[end...]
        }
        return formatter.date(from: String(base))?.addingTimeInterval(fraction)
    }
}
