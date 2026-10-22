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

/// Only the exact standard hook titles are categories. Custom titles remain
/// visible, even when they contain words such as "result" or "attention".
enum InboxCategory {
    case result, attention, failure

    init?(title: String) {
        switch title {
        case "New result", "有新结果": self = .result
        case "Needs your attention", "需要你确认": self = .attention
        case "Run failed", "运行出错": self = .failure
        default: return nil
        }
    }

    var symbol: String {
        switch self {
        case .result: "bubble.left"
        case .attention: "checklist"
        case .failure: "exclamationmark.triangle"
        }
    }
}

extension MeowNotification {
    var inboxCategory: InboxCategory? { InboxCategory(title: inboxTitle) }

    var compactHeadline: String {
        if inboxCategory != nil, let session = context?.sessionLabel { return session }
        return inboxTitle
    }

    var compactMetadata: String {
        // A custom headline carries information: keep its session on the
        // metadata line instead of replacing the headline or adding a fourth row.
        if inboxCategory == nil, let session = context?.sessionLabel {
            return originSummary + " · " + session
        }
        return originSummary
    }
}
