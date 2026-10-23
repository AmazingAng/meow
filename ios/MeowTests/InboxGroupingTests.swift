import XCTest
@testable import Meow

private func groupedItem(_ id: String, source: String = "Codex", workspace: String? = "meow",
                         workspaceID: String? = "w", session: String? = "Review", sessionID: String? = nil,
                         read: Bool = false, time: Double = 1, event: NotificationEvent? = .result,
                         body: String = "Result") -> MeowNotification {
    MeowNotification(id: id, type: .info, title: "New result", body: body, source: source, priority: "normal",
        pushStatus: "accepted", createdAt: Date(timeIntervalSince1970: time), readAt: read ? .now : nil,
        context: NotificationOrigin(workspace: workspace, workspaceId: workspaceID, session: session, sessionId: sessionID), event: event)
}

final class InboxGroupingTests: XCTestCase {
    private func groups(_ items: [MeowNotification], mode: InboxGroupingMode = .sessions,
                        rules: [InboxMergeRule] = [], query: String = "", unread: Bool = false,
                        scope: String = "a") -> [InboxGroup] {
        InboxGrouping.build(items, mode: mode, scope: scope, rules: rules, query: query, unreadOnly: unread)
    }

    func testSessionsUseSourceWorkspaceIdentityAndIDBeforeName() {
        let items = [groupedItem("1", sessionID: "one"), groupedItem("2", sessionID: "two"),
                     groupedItem("3", workspaceID: "other"), groupedItem("4", source: "Claude Code")]
        let result = groups(items)
        XCTAssertEqual(result.count, 4)
        XCTAssertEqual(Set(result.flatMap(\.messages).map(\.id)), Set(["1", "2", "3", "4"]))
    }

    func testSameNameProjectsMergeAcrossPathsAndSourcesButSessionsStaySeparate() {
        let items = [groupedItem("1"), groupedItem("2", source: "Claude Code", workspaceID: "different")]
        XCTAssertEqual(groups(items, mode: .workspaces).count, 1)
        XCTAssertEqual(groups(items).count, 2)
        XCTAssertEqual(groups(items, mode: .sources).count, 2)
    }

    func testTypedIdentitiesDoNotCollideAndMissingFieldsRemainVisible() {
        let items = [groupedItem("1", workspace: "x", workspaceID: "same", session: "same"),
                     groupedItem("2", workspace: "same", workspaceID: nil, session: "same"),
                     groupedItem("3", workspace: "x", workspaceID: "same", session: nil, sessionID: "same"),
                     groupedItem("4", workspace: nil, workspaceID: nil, session: nil),
                     groupedItem("5", workspace: nil, workspaceID: nil, session: "Unclassified")]
        XCTAssertEqual(groups(items).count, 5)
        XCTAssertEqual(groups(items).flatMap(\.messages).count, 5)
        let projects = groups([groupedItem("missing", workspace: nil), groupedItem("named", workspace: "Unclassified")], mode: .workspaces)
        XCTAssertEqual(projects.count, 2)
        XCTAssertEqual(groups([groupedItem("empty", source: "   ")], mode: .sources).first?.title, "Meow")
    }

    func testWhitespaceIsTrimmedButCaseAndRenamedSessionsAreDistinct() {
        let items = [groupedItem("1", session: " Review "), groupedItem("2", session: "Review"),
                     groupedItem("3", session: "review"), groupedItem("4", session: "Renamed", sessionID: "one")]
        XCTAssertEqual(groups(items).count, 3)
    }

    func testAllHistoryIsGroupedBeforeFilteringOrPaginationAndMatchingCountsAreExplicit() {
        var items: [MeowNotification] = []
        for index in 0..<230 {
            let session = index == 0 ? "Archived session" : "Busy session"
            let event: NotificationEvent = index == 229 ? .attention : .result
            items.append(groupedItem("n\(index)", session: session, read: index.isMultiple(of: 2),
                                     time: Double(index), event: event,
                                     body: index == 0 ? "Old matching result" : "Other"))
        }
        let result = groups(items)
        XCTAssertEqual(result.count, 2)
        XCTAssertEqual(result.reduce(0) { $0 + $1.messages.count }, 230)
        XCTAssertEqual(result.first?.unreadCount, 115)
        XCTAssertTrue(result.first!.hasAttention)
        let filtered = groups(items, query: "Old matching")
        XCTAssertEqual(filtered.filter { !$0.matches.isEmpty }.count, 1)
        XCTAssertEqual(filtered.first?.matches.first?.id, "n0")
        XCTAssertEqual(filtered.reduce(0) { $0 + $1.messages.count }, 230)
        XCTAssertEqual(groups(items, unread: true).flatMap(\.matches).count, 115)
    }

    func testMetadataColorAndIdentityAreStableAcrossSearchSortingAndLatestAgent() throws {
        let a = groupedItem("old", source: "Codex", time: 1, body: "needle")
        let b = groupedItem("new", source: "Claude Code", workspaceID: "different", time: 2)
        let normal = try XCTUnwrap(groups([a, b], mode: .workspaces).first)
        let searched = try XCTUnwrap(groups([b, a], mode: .workspaces, query: "needle").first)
        XCTAssertEqual(normal.id, searched.id)
        XCTAssertEqual(normal.title, searched.title)
        XCTAssertEqual(normal.subtitle, searched.subtitle)
        XCTAssertEqual(normal.colorSource, searched.colorSource)
        XCTAssertEqual(normal.colorContext, searched.colorContext)
        XCTAssertEqual(normal.latest?.id, "new")
        XCTAssertEqual(searched.latest?.id, "old")
        XCTAssertEqual(normal.id, groups([a], mode: .workspaces).first?.id)
        XCTAssertNotEqual(normal.id, groups([a], mode: .workspaces, scope: "b").first?.id)
    }

    func testMergeContainsWholeGroupsAndFutureMessagesAndDoesNotAffectOtherModes() throws {
        let items = [groupedItem("1", session: "A"), groupedItem("2", session: "B")]
        let original = groups(items)
        let rule = InboxMergeRule(mode: .sessions, members: original.flatMap(\.members))
        let next = items + [groupedItem("3", session: "A", time: 4, body: "new needle")]
        let merged = try XCTUnwrap(groups(next, rules: [rule], query: "needle").first)
        XCTAssertEqual(merged.messages.count, 3)
        XCTAssertEqual(merged.matches.map(\.id), ["3"])
        XCTAssertEqual(merged.members.count, 2)
        XCTAssertEqual(merged.mergeID, rule.id)
        XCTAssertEqual(groups(next, mode: .workspaces, rules: [rule]).first?.mergeID, nil)
        XCTAssertEqual(groups(next, rules: []).count, 2)
        XCTAssertEqual(groups([], rules: [rule]).count, 0)
        XCTAssertEqual(groups(next, rules: [rule]).first?.id, merged.id)
    }

    func testOverlappingRulesCannotDuplicateMessagesAndMergeColorIsStable() throws {
        let items = [groupedItem("1", session: "A"), groupedItem("2", session: "B"), groupedItem("3", session: "C")]
        let bases = groups(items)
        let a = InboxMergeRule(id: "a", mode: .sessions, members: Array(bases.prefix(2)).flatMap(\.members))
        let b = InboxMergeRule(id: "b", mode: .sessions, members: Array(bases.suffix(2)).flatMap(\.members))
        let result = groups(items, rules: [a, b])
        XCTAssertEqual(result.flatMap(\.messages).count, 3)
        XCTAssertEqual(Set(result.flatMap(\.messages).map(\.id)).count, 3)
        let merged = try XCTUnwrap(result.first { $0.mergeID == "a" })
        let reordered = InboxMergeRule(id: a.id, mode: a.mode, members: a.members.reversed())
        XCTAssertEqual(merged.colorSource, groups(items.reversed(), rules: [reordered]).first { $0.mergeID == "a" }?.colorSource)
    }

    func testEmptyMergeMembersAreHiddenButTheirFutureMessagesRejoin() throws {
        let a = groupedItem("1", session: "A"), b = groupedItem("2", session: "B")
        let rule = InboxMergeRule(mode: .sessions, members: groups([a, b]).flatMap(\.members))
        let remaining = try XCTUnwrap(groups([b], rules: [rule]).first)
        XCTAssertEqual(remaining.title, "B")
        XCTAssertEqual(remaining.visibleMembers.map(\.title), ["B"])
        XCTAssertEqual(remaining.members.count, 2)
        XCTAssertEqual(remaining.mergeID, rule.id)
        let restored = try XCTUnwrap(groups([b, groupedItem("3", session: "A")], rules: [rule]).first)
        XCTAssertEqual(restored.visibleMembers.count, 2)
        XCTAssertEqual(restored.id, remaining.id)
        XCTAssertEqual(restored.messages.count, 2)
    }

    func testStableOrderingDoesNotDependOnInputOrder() {
        let items = [groupedItem("a", session: "A"), groupedItem("z", session: "A"), groupedItem("b", session: "B")]
        let a = groups(items), b = groups(items.reversed())
        XCTAssertEqual(a.map(\.id), b.map(\.id))
        XCTAssertEqual(a.flatMap(\.messages).map(\.id), b.flatMap(\.messages).map(\.id))
    }

    func testPreferencesRoundTripAndScopesAreIndependent() {
        let suite = "grouping-\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let store = InboxGroupingStore(defaults: defaults)
        XCTAssertEqual(store.mode, .messages)
        store.mode = .sessions
        let bases = groups([groupedItem("1", session: "A"), groupedItem("2", session: "B")])
        let rule = InboxMergeRule(mode: .sessions, members: bases.flatMap(\.members))
        store.save([rule], scope: "a")
        let reopened = InboxGroupingStore(defaults: defaults)
        XCTAssertEqual(reopened.load(scope: "a"), [rule])
        XCTAssertTrue(reopened.load(scope: "b").isEmpty)
        XCTAssertEqual(reopened.mode, .sessions)
        reopened.erase(scope: "a")
        XCTAssertTrue(store.load(scope: "a").isEmpty)
        XCTAssertEqual(store.mode, .sessions)
    }

    func testLargeHistoryRetainsEveryMessageAcrossGroups() {
        let items = (0..<10_000).map { groupedItem("n\($0)", session: "Session \($0 % 250)", time: Double($0)) }
        let result = groups(items)
        XCTAssertEqual(result.count, 250)
        XCTAssertEqual(result.flatMap(\.messages).count, items.count)
        XCTAssertEqual(result.first?.latest?.id, "n9999")
    }
}
