import AVFAudio
import UserNotifications
import XCTest
@testable import Meow

final class NotificationSoundTests: XCTestCase {
    func testBundledSoundIsReadableAndShortEnoughForNotificationPlayback() throws {
        let url = try XCTUnwrap(Bundle.main.url(forResource: NotificationSound.filename, withExtension: nil))
        let file = try AVAudioFile(forReading: url)
        let duration = Double(file.length) / file.fileFormat.sampleRate
        XCTAssertGreaterThan(duration, 0)
        XCTAssertLessThan(duration, 30)
        XCTAssertTrue([1, 2].contains(file.fileFormat.channelCount))
        XCTAssertEqual(file.fileFormat.sampleRate, 44_100)
    }

    func testDisplayUsesMeowForDirectAndFallbackNotifications() throws {
        let identity = NotificationIdentity(server: URL(string: "https://origin.example")!, token: "mdt_test",
                                            deviceID: "dev_test", bindingID: "bind_test")
        for relay in [false, true] {
            let original = UNMutableNotificationContent()
            original.sound = .default // compatibility with existing servers
            original.interruptionLevel = .timeSensitive
            original.userInfo = ["meow": ["id": "ntf_sound", "binding_id": "bind_test", "relay": relay]]
            let fallback = NotificationContent.fallback(original, identity: identity)
            let result = NotificationContent.forDisplay(fallback, identity: identity)
            XCTAssertEqual(result.sound, NotificationSound.meow)
            XCTAssertEqual(result.interruptionLevel, .timeSensitive)
            if relay { XCTAssertEqual(result.categoryIdentifier, "") }
        }
    }

    func testSilentNotificationsDoNotAcquireSoundAtDisplay() {
        let content = UNMutableNotificationContent()
        content.interruptionLevel = .passive
        content.sound = nil
        let result = NotificationContent.forDisplay(content, identity: nil)
        XCTAssertNil(result.sound)
        XCTAssertEqual(result.interruptionLevel, .passive)
    }
}
