import Foundation

/// One immutable local result per history/query/filter revision. Views only
/// slice these arrays; searching and urgency classification run in a worker.
struct InboxProjection: Sendable {
    var query = ""
    var unreadOnly = false
    var pendingOnly = false
    var matches: [MeowNotification] = []
    var pending: [MeowNotification] = []
    var activity: [MeowNotification] = []

    static func build(
        _ items: [MeowNotification], query: String, unreadOnly: Bool,
        pendingOnly: Bool, now: Date
    ) -> Self {
        let query = query.trimmingCharacters(in: .whitespacesAndNewlines)
        var matches: [MeowNotification] = []
        for (i, n) in items.enumerated() {
            if i % 256 == 0 && Task.isCancelled { return Self() }
            if pendingOnly && n.question?.isOpen(at: now) != true { continue }
            if InboxGrouping.matches(n, query: query, unreadOnly: unreadOnly) { matches.append(n) }
        }
        let presentation = InboxPresentation(matches, now: now)
        return Self(
            query: query, unreadOnly: unreadOnly, pendingOnly: pendingOnly, matches: matches,
            pending: presentation.pending, activity: presentation.activity)
    }
}
