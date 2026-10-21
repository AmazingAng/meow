import XCTest
@testable import Meow

final class SessionContextTests: XCTestCase {
    func testContextSurvivesJSONAndLegacyMessagesStillDecode() throws {
        let data = Data(#"{"id":"n","type":"info","title":"New result · project","body":"Done","source":"Codex","priority":"normal","pushStatus":"accepted","createdAt":0,"context":{"workspace":"project","workspaceId":"w","session":"部署审查 😺","sessionId":"s1"}}"#.utf8)
        let item = try JSONDecoder().decode(MeowNotification.self, from: data)
        let encoded = try JSONSerialization.jsonObject(with: JSONEncoder().encode(item)) as! [String: Any]
        XCTAssertNotNil(encoded["context"], "context must survive decoding and the local JSON cache")
        var legacy = try JSONSerialization.jsonObject(with: data) as! [String: Any]
        legacy.removeValue(forKey: "context")
        XCTAssertNoThrow(try JSONDecoder().decode(MeowNotification.self, from: JSONSerialization.data(withJSONObject: legacy)))
    }

    private func item() -> MeowNotification {
        MeowNotification(id: "n", type: .info, title: "New result · project", body: "", source: "Codex",
            priority: "normal", pushStatus: "accepted", createdAt: .now,
            context: .init(workspace: "project", workspaceId: "w", session: "部署审查 😺", sessionId: "s"))
    }

    func testSessionLabelsTitleAndLegacyFallback() {
        var n = item()
        XCTAssertEqual(n.context?.sessionLabel, "部署审查 😺")
        XCTAssertEqual(n.originSummary, "Codex · project")
        XCTAssertEqual(n.inboxTitle, "New result")
        n.title = "A custom title"
        XCTAssertEqual(n.inboxTitle, "A custom title")
        n.context?.session = "  "
        n.context?.sessionId = "12345678-long-id"
        XCTAssertTrue(n.context!.sessionLabel!.contains("12345678"))
        let firstIDLabel = n.context!.sessionLabel
        n.context?.sessionId = "12345678-other-id"
        XCTAssertNotEqual(n.context!.sessionLabel, firstIDLabel)
        n.context?.sessionId = "session-1"
        XCTAssertTrue(n.context!.sessionLabel!.hasSuffix("session-1"))
        n.context?.sessionId = nil
        XCTAssertNil(n.context?.sessionLabel)
        n.context = nil
        XCTAssertEqual(n.originSummary, "Codex")
        n.source = ""
        XCTAssertEqual(n.originSummary, "Meow")
        n.title = "New result · project"
        XCTAssertEqual(n.inboxTitle, n.title)
    }

    func testIdentityColorsSurviveRenamesAndStayInWorkspaceFamily() {
        var context = item().context!
        let first = OriginColor(source: "Codex", context: context)
        context.session = "Renamed"; context.workspace = "Renamed project"
        XCTAssertEqual(first, OriginColor(source: "codex", context: context))
        context.sessionId = "another-session"
        let next = OriginColor(source: "Codex", context: context)
        XCTAssertEqual(first.hue, next.hue)
        XCTAssertNotEqual(first, next)
        context.workspaceId = "another-workspace"
        let otherWorkspace = OriginColor(source: "Codex", context: context)
        XCTAssertNotEqual(first.hue, otherWorkspace.hue)
        XCTAssertLessThan(abs(first.hue - otherWorkspace.hue), 0.09)
        let claude = OriginColor(source: "Claude Code", context: context)
        XCTAssertGreaterThan(abs(claude.hue - first.hue), 0.4)
    }

    func testGeneratedColorsHaveAccessibleWhiteContrastAndValidChannels() {
        for i in 0..<512 {
            let context = NotificationOrigin(workspace: "工程 \(i % 7)", sessionId: "会话-\(i)")
            let a = OriginColor(source: "agent-\(i % 17)", context: context)
            XCTAssertEqual(a, OriginColor(source: "agent-\(i % 17)", context: context))
            let rgb = [a.red, a.green, a.blue]
            XCTAssertTrue(rgb.allSatisfy { $0.isFinite && $0 >= 0 && $0 <= 1 })
            let linear = rgb.map { $0 <= 0.04045 ? $0 / 12.92 : pow(($0 + 0.055) / 1.055, 2.4) }
            let luminance = 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2]
            XCTAssertGreaterThanOrEqual(1.05 / (luminance + 0.05), 4.5)
        }
        let empty = NotificationOrigin()
        XCTAssertEqual(empty.workspaceIdentity, "")
        XCTAssertEqual(empty.sessionIdentity, "")
        XCTAssertNotEqual(OriginColor(source: "Claude", context: empty), OriginColor(source: "Codex", context: empty))
    }

    func testOriginCacheRoundTripAndSnakeCaseAPI() async throws {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
        let n = item()
        try await store.save([n], scope: "context-test")
        let restored = try await store.load(scope: "context-test")
        XCTAssertEqual(restored, [n])
        let decoder = JSONDecoder(); decoder.keyDecodingStrategy = .convertFromSnakeCase
        let origin = try decoder.decode(NotificationOrigin.self,
            from: Data(#"{"workspace":"工程","workspace_id":"w","session":"部署","session_id":"s"}"#.utf8))
        XCTAssertEqual(origin.workspaceId, "w"); XCTAssertEqual(origin.sessionId, "s")
        XCTAssertTrue(origin.searchText.contains("部署"))
        let response = Data(#"{"id":"ntf_live","type":"info","title":"Result","body":"Done","source":"Codex","priority":"normal","push_status":"accepted","created_at":"2026-10-21T15:00:00Z","delivered_at":null,"read_at":null,"context":{"workspace":"工程","workspace_id":"w","session":"部署","session_id":"s"}}"#.utf8)
        let received = try MeowJSON.decoder.decode(MeowNotification.self, from: response)
        XCTAssertEqual(received.context, origin)
        XCTAssertEqual(received.originSummary, "Codex · 工程")
    }
}
