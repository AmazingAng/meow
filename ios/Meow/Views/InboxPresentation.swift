import Foundation

/// Groups the loaded page without treating a read question as resolved.
struct InboxPresentation {
    let pending: [MeowNotification]
    let activity: [MeowNotification]

    init(_ notifications: [MeowNotification], now: Date) {
        pending = notifications.filter { $0.question?.isOpen(at: now) == true }
            .sorted {
                guard let left = $0.question?.expiresAt, let right = $1.question?.expiresAt else {
                    return $0.id < $1.id
                }
                return left == right ? $0.id < $1.id : left < right
            }
        let pendingIDs = Set(pending.map(\.id))
        activity = notifications.filter { !pendingIDs.contains($0.id) }
    }
}

/// Event classification is part of the protocol, never inferred from display text.
typealias InboxCategory = NotificationEvent

extension NotificationEvent {
    var symbol: String {
        switch self {
        case .result: "bubble.left"
        case .attention: "checklist"
        case .failure: "exclamationmark.triangle"
        }
    }
}

extension MeowNotification {
    var inboxCategory: InboxCategory? { event }

    var compactHeadline: String {
        if let session = context?.sessionLabel { return session }
        // Event names belong to the icon; never use them as a fallback headline.
        return event == nil ? inboxTitle : String(localized: "Session")
    }

    var compactMetadata: String {
        let agent = NotificationOrigin.nonempty(source) ?? "Meow"
        return [context?.workspaceLabel, agent].compactMap { $0 }.joined(separator: " · ")
    }
}
