import Foundation
import CryptoKit

enum InboxGroupingMode: String, Codable, CaseIterable, Identifiable, Sendable {
    case messages, sessions, workspaces, sources
    var id: String { rawValue }
    var title: String {
        switch self {
        case .messages: String(localized: "Messages")
        case .sessions: String(localized: "Sessions")
        case .workspaces: String(localized: "Projects")
        case .sources: String(localized: "Sources")
        }
    }
    var symbol: String {
        switch self {
        case .messages: "tray"
        case .sessions: "bubble.left.and.bubble.right"
        case .workspaces: "folder"
        case .sources: "antenna.radiowaves.left.and.right"
        }
    }
}

struct InboxGroupMember: Codable, Hashable, Sendable, Identifiable {
    var id: String
    var title: String
    var subtitle: String
}

struct InboxMergeRule: Codable, Sendable, Equatable, Identifiable {
    var id: String = UUID().uuidString
    var mode: InboxGroupingMode
    var members: [InboxGroupMember]
}

struct InboxGroup: Identifiable, Sendable {
    var id: String
    var mode: InboxGroupingMode
    var title: String
    var subtitle: String
    /// Full identities are retained for future messages and subsequent merges.
    var members: [InboxGroupMember]
    var visibleMembers: [InboxGroupMember]
    var mergeID: String?
    var colorSource: String
    var colorContext: NotificationOrigin
    var messages: [MeowNotification]
    var matches: [MeowNotification]

    var unreadCount: Int
    var hasAttention: Bool
    var latest: MeowNotification? { matches.first }
}

/// Pure local projection. IDs encode typed components, never Swift hashValue.
/// All local history enters this function BEFORE matching and UI pagination.
enum InboxGrouping {
    private struct Descriptor {
        var member: InboxGroupMember
        var colorSource: String
        var colorContext: NotificationOrigin
    }

    static func stableID(_ components: [String]) -> String {
        let bytes = (try? JSONEncoder().encode(components)) ?? Data()
        return SHA256.hash(data: bytes).map { String(format: "%02x", $0) }.joined()
    }

    private static func descriptor(_ n: MeowNotification, mode: InboxGroupingMode, scope: String) -> Descriptor {
        let source = NotificationOrigin.nonempty(n.source) ?? "Meow"
        let workspace = NotificationOrigin.nonempty(n.context?.workspace)
        let workspaceID = NotificationOrigin.nonempty(n.context?.workspaceId)
        let session = NotificationOrigin.nonempty(n.context?.session)
        let sessionID = NotificationOrigin.nonempty(n.context?.sessionId)
        var key = [scope, mode.rawValue]
        var title = "", subtitle = "", colorSource = source
        var colorContext = NotificationOrigin()
        switch mode {
        case .sessions:
            if session != nil || sessionID != nil {
                key += ["source", source]
                if let workspaceID { key += ["workspace-id", workspaceID] }
                else if let workspace { key += ["workspace-name", workspace] }
                else { key += ["workspace-missing"] }
                if let session { key += ["session-name", session]; title = session }
                else if let sessionID {
                    key += ["session-id", sessionID]
                    title = NotificationOrigin(sessionId: sessionID).sessionLabel ?? String(localized: "Session")
                }
                subtitle = [workspace, source].compactMap { $0 }.joined(separator: " · ")
                colorContext = NotificationOrigin(workspace: workspace, workspaceId: workspaceID, session: session,
                                                  sessionId: session == nil ? sessionID : nil)
            } else {
                key += ["unclassified"]; title = String(localized: "Unclassified"); colorSource = "unclassified"
            }
        case .workspaces:
            if let workspace {
                key += ["workspace-name", workspace]; title = workspace; colorSource = "workspace:" + workspace
            } else {
                key += ["unclassified"]; title = String(localized: "Unclassified"); colorSource = "unclassified"
            }
        case .sources:
            key += ["source", source]; title = source
        case .messages:
            key += ["message", n.id]; title = n.compactHeadline
        }
        return Descriptor(member: .init(id: "base:" + stableID(key), title: title, subtitle: subtitle),
                          colorSource: colorSource, colorContext: colorContext)
    }

    static func matches(_ n: MeowNotification, query: String, unreadOnly: Bool) -> Bool {
        if unreadOnly && !n.isUnread { return false }
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        return q.isEmpty || n.title.localizedCaseInsensitiveContains(q)
            || n.body.localizedCaseInsensitiveContains(q) || n.source.localizedCaseInsensitiveContains(q)
            || n.context?.searchText.localizedCaseInsensitiveContains(q) == true
    }

    static func build(_ messages: [MeowNotification], mode: InboxGroupingMode, scope: String,
                      rules: [InboxMergeRule], query: String = "", unreadOnly: Bool = false) -> [InboxGroup] {
        guard mode != .messages else { return [] }
        var descriptions: [String: Descriptor] = [:]
        var buckets: [String: [MeowNotification]] = [:]
        var newest: [String: MeowNotification] = [:]
        for (index, n) in messages.enumerated() {
            if index % 256 == 0 && Task.isCancelled { return [] }
            let d = descriptor(n, mode: mode, scope: scope)
            if let old = newest[d.member.id] {
                if n.createdAt > old.createdAt || (n.createdAt == old.createdAt && n.id > old.id) {
                    descriptions[d.member.id] = d; newest[d.member.id] = n
                }
            } else { descriptions[d.member.id] = d; newest[d.member.id] = n }
            buckets[d.member.id, default: []].append(n)
        }
        var owner: [String: InboxMergeRule] = [:]
        for rule in rules.sorted(by: { $0.id < $1.id }) where rule.mode == mode && Set(rule.members.map(\.id)).count >= 2 {
            // Reject overlapping/corrupt rules rather than duplicate messages.
            guard rule.members.allSatisfy({ owner[$0.id] == nil }) else { continue }
            for member in rule.members { owner[member.id] = rule }
        }
        var combined: [String: [MeowNotification]] = [:]
        var identities: [String: String] = [:]
        for (id, items) in buckets {
            let displayID = owner[id].map { "merge:" + $0.id } ?? id
            combined[displayID, default: []] += items
            identities[displayID] = id
        }
        var groups: [InboxGroup] = []
        for (id, items) in combined {
            guard let baseID = identities[id], let d = descriptions[baseID] else { continue }
            let all = items.sorted { $0.createdAt == $1.createdAt ? $0.id > $1.id : $0.createdAt > $1.createdAt }
            let matched = all.filter { matches($0, query: query, unreadOnly: unreadOnly) }
            var members = [d.member], visibleMembers = members, title = d.member.title, subtitle = d.member.subtitle
            var colorSource = d.colorSource, context = d.colorContext
            let merge = owner[baseID]
            if let merge {
                members = merge.members.map { descriptions[$0.id]?.member ?? $0 }.sorted { $0.id < $1.id }
                visibleMembers = members.filter { buckets[$0.id] != nil }
                let names = visibleMembers.map(\.title)
                let first = ListFormatter.localizedString(byJoining: Array(names.prefix(2)))
                title = names.count > 2 ? String(localized: "\(first) · \(names.count) groups") : first
                subtitle = names.count == 1 ? String(localized: "Merged group") : String(localized: "\(names.count) combined groups")
                colorSource = id; context = NotificationOrigin()
            } else if mode == .workspaces {
                let sources = Array(Set(all.map {
                    NotificationOrigin.nonempty($0.source) ?? "Meow"
                })).sorted()
                let first = ListFormatter.localizedString(byJoining: Array(sources.prefix(2)))
                subtitle = sources.count > 2 ? String(localized: "\(first) · \(sources.count) sources") : first
            } else if mode == .sources {
                subtitle = all.lazy.compactMap { $0.context?.workspaceLabel }.first ?? ""
            }
            groups.append(InboxGroup(id: id, mode: mode, title: title, subtitle: subtitle, members: members, visibleMembers: visibleMembers,
                                     mergeID: merge?.id, colorSource: colorSource, colorContext: context,
                                     messages: all, matches: matched, unreadCount: matched.lazy.filter(\.isUnread).count,
                                     hasAttention: matched.contains { $0.isUnread && ($0.event == .attention || $0.event == .failure) }))
        }
        return groups.sorted {
            let a = $0.latest?.createdAt ?? .distantPast, b = $1.latest?.createdAt ?? .distantPast
            return a == b ? $0.id < $1.id : a > b
        }
    }
}

/// Preferences have their own local namespace; no credentials enter the archive.
final class InboxGroupingStore {
    private let defaults: UserDefaults
    private let prefix = "inbox_grouping_v1."
    init(defaults: UserDefaults = MeowShared.defaults) { self.defaults = defaults }
    var mode: InboxGroupingMode {
        get { InboxGroupingMode(rawValue: defaults.string(forKey: prefix + "mode") ?? "") ?? .messages }
        set { defaults.set(newValue.rawValue, forKey: prefix + "mode") }
    }
    func load(scope: String) -> [InboxMergeRule] {
        guard let data = defaults.data(forKey: prefix + scope),
              let rules = try? JSONDecoder().decode([InboxMergeRule].self, from: data) else { return [] }
        return rules
    }
    func save(_ rules: [InboxMergeRule], scope: String) {
        guard let data = try? JSONEncoder().encode(rules) else { return }
        defaults.set(data, forKey: prefix + scope)
    }
    func erase(scope: String) { defaults.removeObject(forKey: prefix + scope) }
}
