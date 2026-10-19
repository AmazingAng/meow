import XCTest
@testable import Meow

/// Used only by verify_local_inbox.py, with a real isolated Go server.
@MainActor
final class LocalInboxLiveIntegrationTests: XCTestCase {
    func testRealServerHistoryIsPersistedAndSearchedBeyondFirstDisplayPage() async throws {
        let url = try XCTUnwrap(ProcessInfo.processInfo.environment["MEOW_RECOVERY_INTEGRATION_URL"].flatMap(URL.init(string:)))
        let oldServer = MeowShared.server, oldToken = MeowShared.deviceToken, oldDevice = MeowShared.deviceID
        let oldKey = Keychain.get("device_key"), oldKeyID = MeowShared.defaults.string(forKey: "device_key_id")
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer {
            MeowShared.server = oldServer; MeowShared.deviceToken = oldToken; MeowShared.deviceID = oldDevice
            Keychain.set(oldKey, for: "device_key"); MeowShared.defaults.set(oldKeyID, forKey: "device_key_id")
            try? FileManager.default.removeItem(at: folder)
        }
        MeowShared.server = url; MeowShared.deviceToken = nil; MeowShared.deviceID = nil
        let store = InboxStore(fileURL: folder.appendingPathComponent("inbox.sqlite"))
        let model = AppModel(inboxStore: store)
        await model.register()
        XCTAssertTrue(model.isRegistered)
        let key = try XCTUnwrap(model.deviceKey), token = try XCTUnwrap(MeowShared.deviceToken)
        let api = MeowAPI(server: url, token: key)
        var expected: String?
        for index in 0..<52 {
            let message = try await api.notify(title: index == 0 ? "离线历史测试 😺" : "Message \(index)", body: "Local synchronization integration")
            if index == 0 { expected = message.id }
        }
        await model.refresh()
        XCTAssertNil(model.loadError)
        XCTAssertEqual(model.notifications.count, 50)
        XCTAssertNotNil(model.nextCursor)
        model.search = "历史测试"
        XCTAssertEqual(model.notifications.map(\.id), [try XCTUnwrap(expected)])
        let persisted = try await store.load(scope: InboxStore.scope(server: url, token: token))
        XCTAssertEqual(persisted?.count, 52)
        XCTAssertEqual(persisted?.first(where: { $0.id == expected })?.title, "离线历史测试 😺")
        await model.markRead(try XCTUnwrap(expected))
        let read = try await store.load(scope: InboxStore.scope(server: url, token: token))
        XCTAssertEqual(read?.first(where: { $0.id == expected })?.isUnread, false)
    }
}
