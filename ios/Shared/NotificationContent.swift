import Foundation
import UserNotifications

enum NotificationSound {
    static let filename = "meow.wav"
    static var meow: UNNotificationSound {
        UNNotificationSound(named: UNNotificationSoundName(rawValue: filename))
    }
}

/// The same stored selection is read by the app and notification extension.
enum NotificationSoundChoice: String, CaseIterable, Identifiable, Sendable {
    case meow, system

    var id: Self { self }

    var title: String {
        switch self {
        case .meow: String(localized: "Cat sound")
        case .system: String(localized: "System default")
        }
    }

    var sound: UNNotificationSound {
        switch self {
        case .meow: NotificationSound.meow
        case .system: .default
        }
    }
}

/// Notification categories and actions. The category identifiers match the
/// ones the server puts in each push (server/internal/push/payload.go).
enum NotificationCategory {
    static let info = "MEOW_INFO"
    static let reply = "MEOW_REPLY"
    static let approval = "MEOW_APPROVAL"
    static let choice = "MEOW_CHOICE" // legacy custom-choice payloads

    static let approveAction = "APPROVE"
    static let rejectAction = "REJECT"
    static let replyAction = "REPLY"

    static var all: Set<UNNotificationCategory> {
        // Register legacy identifiers without actions too, so notifications from
        // older servers cannot restore reply/approval buttons in V1.
        Set([info, reply, approval, choice].map {
            UNNotificationCategory(identifier: $0, actions: [], intentIdentifiers: [])
        })
    }
}

/// A relay wake is bound to one app/server registration, never a payload URL.
struct NotificationIdentity: Equatable, Sendable {
    var server: URL
    var token: String
    var deviceID: String?
    var bindingID: String?

    static var current: Self? {
        guard let server = MeowShared.server, let token = MeowShared.deviceToken else { return nil }
        return Self(server: server, token: token, deviceID: MeowShared.deviceID,
                    bindingID: MeowShared.gatewayBinding.flatMap {
                        $0.serverURL == server && $0.deviceID == MeowShared.deviceID ? $0.bindingID : nil
                    })
    }

    func accepts(_ metadata: [String: Any]) -> Bool {
        guard let id = metadata["id"] as? String,
              id.range(of: #"^ntf_[a-zA-Z0-9_-]{1,64}$"#, options: .regularExpression) != nil else { return false }
        if metadata["relay"] as? Bool == true {
            guard let bindingID, metadata["binding_id"] as? String == bindingID else { return false }
        } else {
            // An unscoped legacy notification cannot be assigned to whichever
            // server happens to be active when the user taps it.
            guard let deviceID, metadata["device_id"] as? String == deviceID else { return false }
        }
        if let expected = metadata["device_id"] as? String, expected != deviceID { return false }
        return true
    }
}

enum NotificationContent {
    // Unicode scalars match Go runes; shared fixtures keep relay/direct previews aligned.
    static func truncatePreview(_ text: String) -> String {
        let scalars = text.unicodeScalars
        return scalars.count > 180 ? String(String.UnicodeScalarView(scalars.prefix(179))) + "…" : text
    }

    static func preview(_ body: String) -> String {
        var text = body.replacingOccurrences(of: "\r\n", with: "\n")
            .replacingOccurrences(of: "\r", with: "\n")
            .trimmingCharacters(in: .whitespacesAndNewlines)
        if text.isEmpty { return "" }
        if (text.hasPrefix("{") || text.hasPrefix("[")),
           let data = text.data(using: .utf8), (try? JSONSerialization.jsonObject(with: data)) != nil { return "" }
        text = plainMarkdown(text)
        let lines = text.components(separatedBy: "\n")
        if lines.count > 3 { text = lines.prefix(3).joined(separator: "\n") + "…" }
        return truncatePreview(text)
    }

    /// System notification banners accept plain text. Parse the full Markdown
    /// before truncating, while keeping code literals and escaped punctuation.
    private static func plainMarkdown(_ source: String) -> String {
        guard let parsed = try? AttributedString(markdown: source) else { return source }
        var result = ""
        var previous: [PresentationIntent.IntentType] = []
        for run in parsed.runs {
            let intents = run.presentationIntent?.components ?? []
            if intents.contains(where: {
                if case .codeBlock = $0.kind { return true }
                return $0.kind == .thematicBreak
            }) { continue }
            let value = String(parsed[run.range].characters)
            guard !value.isEmpty else { continue }
            if !result.isEmpty && intents != previous {
                let row = intents.first {
                    if case .tableRow = $0.kind { return true }
                    return $0.kind == .tableHeaderRow
                }
                result += row != nil && previous.contains(row!) ? " · " : "\n"
            }
            if run.inlinePresentationIntent?.contains(.softBreak) == true ||
                run.inlinePresentationIntent?.contains(.lineBreak) == true {
                result += "\n"
            } else {
                result += value
            }
            previous = intents
        }
        return result.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// A failed fetch never exposes actions or trusts content from a wake payload.
    static func fallback(_ original: UNNotificationContent, identity: NotificationIdentity?) -> UNMutableNotificationContent {
        let content = original.mutableCopy() as! UNMutableNotificationContent
        guard let m = original.userInfo["meow"] as? [String: Any] else { return content }
        let accepted = identity.map { $0.accepts(m) } == true
        let relay = m["relay"] as? Bool == true
        if !relay && accepted { return content }
        content.title = "Meow"
        content.subtitle = ""
        content.body = String(localized: "New message. Open Meow to view.")
        content.categoryIdentifier = ""
        content.threadIdentifier = ""
        content.badge = nil
        content.userInfo = [:]
        if relay && accepted {
            content.userInfo = ["meow": ["id": m["id"]!, "binding_id": m["binding_id"]!, "relay": true]]
        }
        return content
    }

    static func hydrate(_ n: MeowNotification, original: UNNotificationContent,
                        identity: NotificationIdentity, now: Date = .now) -> UNMutableNotificationContent? {
        guard let m = original.userInfo["meow"] as? [String: Any], identity.accepts(m),
              m["relay"] as? Bool == true, m["id"] as? String == n.id else { return nil }
        let content = fallback(original, identity: identity)
        content.title = n.source.isEmpty ? n.title : n.source
        let summary = preview(n.body)
        let body = n.source.isEmpty ? summary : [n.title, summary].filter { !$0.isEmpty }.joined(separator: "\n")
        content.body = truncatePreview(body)
        content.threadIdentifier = n.source
        // Use the origin's priority after hydration, rather than inheriting an
        // interruption level or sound from the generic gateway wake.
        switch n.priority {
        case "low":
            content.interruptionLevel = .passive
            content.sound = nil
        case "high":
            content.interruptionLevel = .timeSensitive
            content.sound = .default
        default:
            content.interruptionLevel = .active
            content.sound = .default
        }
        content.categoryIdentifier = NotificationCategory.info
        var metadata: [String: Any] = ["id": n.id, "relay": true, "binding_id": identity.bindingID!, "hydrated": true]
        if let q = n.question, q.isOpen(at: now) {
            metadata["question_id"] = q.id
            metadata["expires_at_epoch"] = q.expiresAt.timeIntervalSince1970
            if let choices = q.choices { metadata["choices"] = choices }
            content.categoryIdentifier = q.choices == nil ? NotificationCategory.reply
                : q.isApproval ? NotificationCategory.approval : NotificationCategory.choice
        }
        content.userInfo = ["meow": metadata]
        return content
    }

    /// Recheck expiry after the extension has reported receipt and fetched counts.
    static func forDisplay(_ content: UNNotificationContent, identity: NotificationIdentity?, now: Date = .now) -> UNNotificationContent {
        let updated = fallback(content, identity: identity)
        // Hydrated content has already been fetched from the origin. Preserve it
        // only while its original registration is still current.
        if let m = content.userInfo["meow"] as? [String: Any],
           m["relay"] as? Bool == true, m["hydrated"] as? Bool == true,
           identity.map({ $0.accepts(m) }) == true {
            return displayVerified(content, identity: identity, now: now)
        }
        return displayVerified(updated, identity: identity, now: now)
    }

    private static func displayVerified(_ content: UNNotificationContent, identity: NotificationIdentity?, now: Date) -> UNNotificationContent {
        let updated = content.mutableCopy() as! UNMutableNotificationContent
        // Older direct servers send raw Markdown. New servers mark their
        // already rendered preview, so literal asterisks aren't parsed twice.
        if let metadata = content.userInfo["meow"] as? [String: Any],
           metadata["relay"] as? Bool != true,
           metadata["preview_format"] as? String != "plain",
           identity.map({ $0.accepts(metadata) }) == true {
            updated.body = preview(content.body)
            var marked = metadata
            marked["preview_format"] = "plain"
            updated.userInfo = ["meow": marked]
        }
        // Also covers direct pushes, offline fallback and older servers that
        // still send the default system sound. Silent notifications stay silent.
        if updated.sound != nil { updated.sound = MeowShared.notificationSound.sound }
        guard var m = updated.userInfo["meow"] as? [String: Any],
              m["question_id"] != nil,
              identity.map({ canRespond(m, identity: $0, now: now) }) != true else { return updated }
        updated.categoryIdentifier = NotificationCategory.info
        m.removeValue(forKey: "question_id")
        m.removeValue(forKey: "choices")
        updated.userInfo = ["meow": m]
        return updated
    }

    static func canRespond(_ m: [String: Any], identity: NotificationIdentity, now: Date = .now) -> Bool {
        guard identity.accepts(m), m["question_id"] is String else { return false }
        if m["relay"] as? Bool == true {
            guard m["hydrated"] as? Bool == true,
                  let expiry = m["expires_at_epoch"] as? Double, expiry > now.timeIntervalSince1970 else { return false }
        } else if let text = m["expires_at"] as? String {
            guard let expiry = MeowJSON.parseDate(text), expiry > now else { return false }
        }
        return true
    }
}
