import XCTest
@testable import Meow

/// Only included by verify_bounded_cache.py, with its isolated real Go server.
@MainActor
final class CacheLiveIntegrationTests: XCTestCase {
    func testServerPrunesContentWhileRestartedIPhoneHistoryRemainsReadableAndDeletable() async throws {
        let path = try XCTUnwrap(ProcessInfo.processInfo.environment["MEOW_CACHE_INTEGRATION_FIXTURE"])
        struct Fixture: Decodable {
            let url: URL
            let token: String
            let device: String
            let oldID: String
            let recentID: String
            let folder: URL
        }
        let fixture = try JSONDecoder().decode(Fixture.self, from: Data(contentsOf: URL(fileURLWithPath: path)))
        let previousServer = MeowShared.server, previousToken = MeowShared.deviceToken, previousDevice = MeowShared.deviceID
        defer { MeowShared.server = previousServer; MeowShared.deviceToken = previousToken; MeowShared.deviceID = previousDevice }
        MeowShared.server = fixture.url; MeowShared.deviceToken = fixture.token; MeowShared.deviceID = fixture.device
        let file = fixture.folder.appendingPathComponent("phone.sqlite")
        let store = InboxStore(fileURL: file)
        let model = AppModel(inboxStore: store)
        await model.refresh()
        XCTAssertNil(model.loadError)
        XCTAssertEqual(Set(model.savedNotifications.map(\.id)), [fixture.oldID, fixture.recentID])
        let initial = try await store.load(scope: InboxStore.scope(server: fixture.url, token: fixture.token))
        XCTAssertEqual(initial?.count, 2)
        try Data().write(to: fixture.folder.appendingPathComponent("history-saved"))
        let deadline = Date.now.addingTimeInterval(20)
        while !FileManager.default.fileExists(atPath: fixture.folder.appendingPathComponent("server-pruned").path) {
            if Date.now > deadline { XCTFail("server restart/cleanup coordinator did not finish"); return }
            try await Task.sleep(for: .milliseconds(50))
        }
        let restartedStore = InboxStore(fileURL: file)
        let restarted = AppModel(inboxStore: restartedStore)
        await restarted.refresh()
        XCTAssertNil(restarted.loadError)
        XCTAssertEqual(Set(restarted.savedNotifications.map(\.id)), [fixture.oldID, fixture.recentID])
        XCTAssertEqual(restarted.counts.unread, 2)
        restarted.search = "旧消息"
        XCTAssertEqual(restarted.notifications.map(\.id), [fixture.oldID])
        XCTAssertEqual(restarted.notification(id: fixture.oldID)?.body, "**完整历史** 😺\n```go\nrun()\n```")
        let found = await restarted.loadDetail(fixture.oldID)
        XCTAssertTrue(found, "real server 404 must not destroy the saved detail")
        await restarted.markRead(fixture.oldID)
        XCTAssertNil(restarted.toast)
        XCTAssertEqual(restarted.notification(id: fixture.oldID)?.isUnread, false)
        XCTAssertEqual(restarted.counts.unread, 1)
        let read = try await restartedStore.load(scope: InboxStore.scope(server: fixture.url, token: fixture.token))
        XCTAssertEqual(read?.first { $0.id == fixture.oldID }?.isUnread, false)
        await restarted.deleteNotification(fixture.oldID)
        XCTAssertNil(restarted.alert)
        XCTAssertNil(restarted.notification(id: fixture.oldID))
        await restarted.refresh()
        XCTAssertEqual(restarted.savedNotifications.map(\.id), [fixture.recentID])
        await restarted.deleteNotifications(in: InboxDeleteRange.all.interval())
        XCTAssertTrue(restarted.savedNotifications.isEmpty)
        let deleted = try await restartedStore.load(scope: InboxStore.scope(server: fixture.url, token: fixture.token))
        XCTAssertEqual(deleted, [])
    }
}
