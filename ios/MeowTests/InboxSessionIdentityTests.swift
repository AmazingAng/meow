import XCTest
@testable import Meow

private func identityItem(_ id: String, session: String?, sessionID: String? = "stable", time: Double = 1,
                          read: Bool = false, body: String = "Result") -> MeowNotification {
    MeowNotification(id: id, type: .info, title: "New result", body: body, source: "Codex", priority: "normal",
        pushStatus: "accepted", createdAt: Date(timeIntervalSince1970: time), readAt: read ? .now : nil,
        context: NotificationOrigin(workspace: "meow", workspaceId: "w", session: session, sessionId: sessionID), event: .result)
}

/// Frozen build-21 member wire format: no identity-version field.
private func legacyMember(_ name: String) throws -> InboxGroupMember {
    let id = "base:" + InboxGrouping.stableID(["a", "sessions", "source", "Codex", "workspace-id", "w", "session-name", name])
    return try JSONDecoder().decode(InboxGroupMember.self, from: JSONSerialization.data(withJSONObject:
        ["id": id, "title": name, "subtitle": "meow · Codex"]))
}

final class InboxSessionIdentityTests: XCTestCase {
    private func groups(_ items: [MeowNotification], rules: [InboxMergeRule] = [], query: String = "", unread: Bool = false) -> [InboxGroup] {
        InboxGrouping.build(items, mode: .sessions, scope: "a", rules: rules, query: query, unreadOnly: unread)
    }

    func testSessionIDSurvivesMissingNamesAndRenames() throws {
        let a = identityItem("1", session: nil, time: 1, body: "first"),
            b = identityItem("2", session: "Initial title", time: 2),
            c = identityItem("3", session: "Renamed title", time: 3),
            d = identityItem("4", session: nil, time: 4)
        let original = try XCTUnwrap(groups([a]).first)
        let renamed = groups([a, b, c, d])
        XCTAssertEqual(renamed.count, 1)
        let final = try XCTUnwrap(renamed.first)
        XCTAssertEqual(final.id, original.id)
        XCTAssertEqual(final.title, "Renamed title")
        XCTAssertEqual(final.messages.map(\.id), ["4", "3", "2", "1"])
        XCTAssertEqual(OriginColor(source: final.colorSource, context: final.colorContext),
                       OriginColor(source: original.colorSource, context: original.colorContext))
        XCTAssertEqual(groups([a, b, c, d, identityItem("5", session: "Renamed title", sessionID: "other", time: 5)]).count, 2)
    }

    func testSessionNameSelectionIsStableAcrossOrderingSearchAndUnreadFilter() throws {
        let items = [identityItem("a", session: "Old", time: 1, body: "needle"),
                     identityItem("b", session: "Earlier tie", time: 2, read: true),
                     identityItem("z", session: "Latest name", time: 2, read: true),
                     identityItem("new", session: nil, time: 3, read: true)]
        for input in [items, items.reversed(), [items[2], items[0], items[3], items[1]]] {
            let result = groups(Array(input), query: "needle", unread: true)
            XCTAssertEqual(result.count, 1)
            XCTAssertEqual(result.first?.title, "Latest name")
            XCTAssertEqual(result.first?.matches.map(\.id), ["a"])
        }
    }

    func testNoIDFallbackDoesNotGuessAssociationWithNamedIDSession() {
        let items = [identityItem("missing", session: "Same", sessionID: nil),
                     identityItem("one", session: "Same", sessionID: "one"),
                     identityItem("two", session: "Same", sessionID: "two")]
        XCTAssertEqual(groups(items).count, 3)
    }

    func testLegacySessionMergesMigrateWithoutLosingMembers() throws {
        let items = [identityItem("a1", session: "A", sessionID: "one", read: true),
                     identityItem("a2", session: "A", sessionID: "two"),
                     identityItem("b", session: "B", sessionID: "three")]
        let rule = InboxMergeRule(id: "saved-rule", mode: .sessions, members: [try legacyMember("A"), try legacyMember("B")])
        let merged = groups(items, rules: [rule])
        XCTAssertEqual(merged.count, 1)
        XCTAssertEqual(merged.first?.mergeID, rule.id)
        XCTAssertEqual(merged.first?.members.count, 3)
        XCTAssertEqual(merged.first?.unreadCount, 2)
        XCTAssertEqual(Set(merged.flatMap(\.messages).map(\.id)), Set(items.map(\.id)))
        XCTAssertEqual(Set(merged.first?.members.map(\.id) ?? []), Set(groups(items).map(\.id)))
    }

    func testOverlappingMigratedRulesCoalesceDeterministically() throws {
        let items = [identityItem("old", session: "Old", sessionID: "same", time: 1),
                     identityItem("new", session: "New", sessionID: "same", time: 2),
                     identityItem("a", session: "A", sessionID: "a"),
                     identityItem("b", session: "B", sessionID: "b")]
        let rules = [InboxMergeRule(id: "a", mode: .sessions, members: [try legacyMember("Old"), try legacyMember("A")]),
                     InboxMergeRule(id: "b", mode: .sessions, members: [try legacyMember("New"), try legacyMember("B")])]
        let result = groups(items, rules: rules)
        XCTAssertEqual(result.count, 1)
        XCTAssertEqual(result.first?.mergeID, "a")
        XCTAssertEqual(result.first?.members.count, 3)
        XCTAssertEqual(Set(result.flatMap(\.messages).map(\.id)), Set(items.map(\.id)))
        let reversed = groups(items.reversed(), rules: rules.reversed())
        XCTAssertEqual(result.map(\.id), reversed.map(\.id))
        XCTAssertEqual(result.first?.members, reversed.first?.members)
    }
    func testLegacyMergeMigrationWaitsForMissingHistory() throws {
        let rule = InboxMergeRule(id: "saved", mode: .sessions, members: [try legacyMember("A"), try legacyMember("B")])
        let empty = InboxGrouping.project([], mode: .sessions, scope: "a", rules: [rule])
        XCTAssertEqual(empty.rules, [rule])
        let reversed = InboxMergeRule(id: rule.id, mode: rule.mode, members: rule.members.reversed())
        XCTAssertEqual(InboxGrouping.project([], mode: .sessions, scope: "a", rules: [reversed]).rules, [reversed])
        let a = identityItem("a", session: "A", sessionID: "one")
        let partial = InboxGrouping.project([a], mode: .sessions, scope: "a", rules: [rule])
        XCTAssertEqual(partial.rules.first?.members.count, 2)
        XCTAssertEqual(partial.rules.first?.members.filter { $0.sessionIdentityVersion == nil }.count, 1)
        let reloaded = try JSONDecoder().decode([InboxMergeRule].self, from: JSONEncoder().encode(partial.rules))
        let future = [identityItem("renamed", session: "New A", sessionID: "one"),
                      identityItem("b", session: "B", sessionID: "two")]
        let complete = InboxGrouping.project(future, mode: .sessions, scope: "a", rules: reloaded)
        XCTAssertEqual(complete.groups.count, 1)
        XCTAssertEqual(complete.groups.first?.mergeID, "saved")
        XCTAssertEqual(complete.rules.first?.members.filter { $0.sessionIdentityVersion == 2 }.count, 2)
        XCTAssertEqual(InboxGrouping.project(future.reversed(), mode: .sessions, scope: "a", rules: complete.rules).rules, complete.rules)
        XCTAssertEqual(InboxGrouping.project(future, mode: .sources, scope: "a", rules: [rule]).rules, [rule])
    }

    func testLegacyMigrationRetainsLatestKnownNameWhenNewestMessageHasNoName() throws {
        let items = [identityItem("a", session: "Old", time: 1),
                     identityItem("b", session: "Renamed", time: 2),
                     identityItem("c", session: nil, time: 3),
                     identityItem("other", session: "Other", sessionID: "other", time: 4)]
        let rule = InboxMergeRule(id: "saved", mode: .sessions, members: [try legacyMember("Old"), try legacyMember("Other")])
        let result = InboxGrouping.project(items, mode: .sessions, scope: "a", rules: [rule])
        XCTAssertEqual(Set(result.rules.first?.members.map(\.title) ?? []), ["Renamed", "Other"])
        XCTAssertEqual(Set(result.groups.first?.members.map(\.title) ?? []), ["Renamed", "Other"])
        XCTAssertEqual(result.groups.count, 1)
    }

    func testRedundantLegacyMergeCollapsesToNaturalIDGroup() throws {
        let items = [identityItem("old", session: "Old", time: 1), identityItem("new", session: "New", time: 2)]
        let rule = InboxMergeRule(id: "saved", mode: .sessions, members: [try legacyMember("Old"), try legacyMember("New")])
        let result = InboxGrouping.project(items, mode: .sessions, scope: "a", rules: [rule])
        XCTAssertEqual(result.groups.count, 1)
        XCTAssertEqual(result.groups.first?.title, "New")
        XCTAssertNil(result.groups.first?.mergeID)
        XCTAssertTrue(result.rules.isEmpty)
    }

    func testCurrentNameFallbackMergeIsNotExpandedIntoNewSameNamedSessions() throws {
        let initial = [identityItem("a", session: "A", sessionID: nil), identityItem("b", session: "B", sessionID: nil)]
        let rule = InboxMergeRule(mode: .sessions, members: groups(initial).flatMap(\.members))
        let future = initial + [identityItem("new", session: "A", sessionID: "different")]
        let result = groups(future, rules: [rule])
        XCTAssertEqual(result.count, 2)
        XCTAssertEqual(result.first { $0.mergeID != nil }?.messages.count, 2)
        XCTAssertEqual(result.first { $0.mergeID == nil }?.messages.map(\.id), ["new"])
    }

    func testCancelledProjectionDoesNotMigratePartialRulesOrPublishGroups() async throws {
        let rule = InboxMergeRule(id: "saved", mode: .sessions, members: [try legacyMember("A"), try legacyMember("B")])
        let work = Task { () -> (groups: [InboxGroup], rules: [InboxMergeRule]) in
            // Yield until cancellation is visible; cancellation must preserve the original archive.
            while !Task.isCancelled { await Task.yield() }
            return InboxGrouping.project([identityItem("a", session: "A"), identityItem("b", session: "B")],
                                         mode: .sessions, scope: "a", rules: [rule])
        }
        work.cancel()
        let result = await work.value
        XCTAssertTrue(result.groups.isEmpty)
        XCTAssertEqual(result.rules, [rule])
        XCTAssertTrue(InboxGrouping.build([identityItem("a", session: "A")], mode: .messages, scope: "a", rules: []).isEmpty)
    }

    func testGeneratedHistoryConservesMessagesAndIdentityAcrossReordering() {
        let items = (0..<140).map { index in
            identityItem("n\(index)", session: index.isMultiple(of: 5) ? nil : "Name \(index % 3)",
                         sessionID: "id-\(index % 7)", time: Double(index))
        }
        let expected = groups(items)
        XCTAssertEqual(expected.count, 7)
        for input in [Array(items.reversed()), Array(items.dropFirst(39)) + Array(items.prefix(39)),
                      items.enumerated().filter { $0.offset.isMultiple(of: 2) }.map(\.element) + items.enumerated().filter { !$0.offset.isMultiple(of: 2) }.map(\.element)] {
            let actual = groups(input)
            XCTAssertEqual(actual.map(\.id), expected.map(\.id))
            XCTAssertEqual(actual.map(\.title), expected.map(\.title))
            XCTAssertEqual(actual.flatMap(\.messages).map(\.id), expected.flatMap(\.messages).map(\.id))
            XCTAssertEqual(Set(actual.flatMap(\.messages).map(\.id)).count, items.count)
        }
    }

}
