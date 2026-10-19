import XCTest
@testable import Meow

/// Only the native runner includes this test, with its own ephemeral Go server.
@MainActor
final class RecoveryLiveIntegrationTests: XCTestCase {
    func testActualServerRegistrationReadAndPermanentReplyFailure() async throws {
        let server = try XCTUnwrap(ProcessInfo.processInfo.environment["MEOW_RECOVERY_INTEGRATION_URL"].flatMap(URL.init(string:)))
        let oldServer = MeowShared.server
        let oldToken = MeowShared.deviceToken
        let oldDevice = MeowShared.deviceID
        let oldKey = Keychain.get("device_key")
        let oldKeyID = MeowShared.defaults.string(forKey: "device_key_id")
        let oldOnboarded = MeowShared.defaults.bool(forKey: "onboarded")
        let file = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString + ".json")
        defer {
            MeowShared.server = oldServer; MeowShared.deviceToken = oldToken; MeowShared.deviceID = oldDevice
            Keychain.set(oldKey, for: "device_key")
            MeowShared.defaults.set(oldKeyID, forKey: "device_key_id")
            MeowShared.defaults.set(oldOnboarded, forKey: "onboarded")
            try? FileManager.default.removeItem(at: file)
        }
        MeowShared.server = server; MeowShared.deviceToken = nil
        let queue = ReplyQueue(fileURL: file) { reply in
            try await MeowAPI(server: server, token: MeowShared.deviceToken)
                .respond(questionID: reply.questionID, answer: reply.answer, idempotencyKey: reply.idempotencyKey)
        }
        let model = AppModel(replies: queue)
        await model.register()
        XCTAssertTrue(model.isRegistered)
        let firstDevice = try XCTUnwrap(MeowShared.deviceID)
        let firstKey = try XCTUnwrap(model.deviceKey)
        let sent = try await MeowAPI(server: server, token: firstKey).notify(title: "Recovery integration", body: "Local isolated server")
        await model.refresh()
        XCTAssertEqual(model.counts.unread, 1)
        await model.markRead(sent.id)
        await model.refresh()
        XCTAssertEqual(model.counts.unread, 0)
        XCTAssertEqual(model.notification(id: sent.id)?.isUnread, false)

        let original = PendingReply(idempotencyKey: "ios-live-integration", questionID: "qst_does_not_exist", answer: "original live answer", createdAt: .now)
        try JSONEncoder().encode([original]).write(to: file)
        let restarted = ReplyQueue(fileURL: file) { reply in
            try await MeowAPI(server: server, token: MeowShared.deviceToken)
                .respond(questionID: reply.questionID, answer: reply.answer, idempotencyKey: reply.idempotencyKey)
        }
        let restored = AppModel(replies: restarted)
        await restored.flushReplies()
        XCTAssertNotNil(restored.alert)
        XCTAssertEqual(restored.failedReplies.first?.answer, original.answer)
        XCTAssertEqual(restored.failedReplies.first?.failure?.status, 404)
        let reset = await restored.reset(server: server)
        XCTAssertTrue(reset)
        await restored.register()
        XCTAssertTrue(restored.isRegistered)
        XCTAssertNotEqual(MeowShared.deviceID, firstDevice)
        XCTAssertNotEqual(restored.deviceKey, firstKey)
        XCTAssertEqual(restored.server, server)
        XCTAssertTrue(restored.failedReplies.isEmpty)
    }
}
