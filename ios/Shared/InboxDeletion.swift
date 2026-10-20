import Foundation

struct InboxDeletionInterval: Equatable, Sendable {
    let from: Int64
    let before: Int64

    func contains(_ date: Date) -> Bool {
        let milliseconds = Int64((date.timeIntervalSince1970 * 1_000).rounded())
        return milliseconds >= from && milliseconds < before
    }
}

enum InboxDeleteRange: String, CaseIterable, Identifiable {
    case lastHour, today, todayAndYesterday, lastMonth, all
    case beforeOneHour, beforeToday, beforeYesterday, beforeOneMonth
    var id: Self { self }
    static let recent: [Self] = [.lastHour, .today, .todayAndYesterday, .lastMonth, .all]
    static let older: [Self] = [.beforeOneHour, .beforeToday, .beforeYesterday, .beforeOneMonth]

    var title: String {
        switch self {
        case .lastHour: String(localized: "Last hour")
        case .today: String(localized: "Today")
        case .todayAndYesterday: String(localized: "Today and yesterday")
        case .lastMonth: String(localized: "Last month")
        case .all: String(localized: "All messages")
        case .beforeOneHour: String(localized: "Before the last hour")
        case .beforeToday: String(localized: "Before today")
        case .beforeYesterday: String(localized: "Before yesterday")
        case .beforeOneMonth: String(localized: "Older than a month")
        }
    }

    func interval(now: Date = .now, calendar: Calendar = .current) -> InboxDeletionInterval {
        let milliseconds = Int64(now.timeIntervalSince1970 * 1_000)
        let today = calendar.startOfDay(for: now)
        let yesterday = calendar.date(byAdding: .day, value: -1, to: today)!
        let monthAgo = calendar.date(byAdding: .month, value: -1, to: now)!
        let cutoff: Int64
        switch self {
        case .lastHour, .beforeOneHour: cutoff = milliseconds - 3_600_000
        case .today, .beforeToday: cutoff = Int64(today.timeIntervalSince1970 * 1_000)
        case .todayAndYesterday, .beforeYesterday: cutoff = Int64(yesterday.timeIntervalSince1970 * 1_000)
        case .lastMonth, .beforeOneMonth: cutoff = Int64(monthAgo.timeIntervalSince1970 * 1_000)
        case .all: cutoff = 0
        }
        if Self.older.contains(self) { return .init(from: 0, before: cutoff) }
        // Include records created during the selection millisecond. The fixed
        // upper bound is retained while the user reads the confirmation.
        return .init(from: cutoff, before: milliseconds + 1)
    }
}
