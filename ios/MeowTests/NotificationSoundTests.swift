import AVFAudio
import UserNotifications
import XCTest
@testable import Meow

final class NotificationSoundTests: XCTestCase {
    private var previousSound: Any?

    override func setUp() {
        previousSound = MeowShared.defaults.object(forKey: MeowShared.notificationSoundKey)
        MeowShared.defaults.removeObject(forKey: MeowShared.notificationSoundKey)
    }

    override func tearDown() {
        MeowShared.defaults.set(previousSound, forKey: MeowShared.notificationSoundKey)
    }

    func testMissingAndUnknownPreferencesKeepExistingCatSound() {
        XCTAssertEqual(MeowShared.notificationSound, .meow)
        MeowShared.defaults.set("future-tone", forKey: MeowShared.notificationSoundKey)
        XCTAssertEqual(MeowShared.notificationSound, .meow)
    }

    func testSelectionIsStoredInAppGroupAndCanBeReadFromAnotherDefaultsInstance() throws {
        MeowShared.notificationSound = .system
        let extensionDefaults = try XCTUnwrap(UserDefaults(suiteName: MeowShared.appGroup))
        XCTAssertEqual(extensionDefaults.string(forKey: MeowShared.notificationSoundKey), "system")
        XCTAssertEqual(MeowShared.notificationSound, .system)
        extensionDefaults.set("meow", forKey: MeowShared.notificationSoundKey)
        XCTAssertEqual(MeowShared.notificationSound, .meow)
    }

    func testDirectAndOfflineFallbackNotificationsUseChosenSound() {
        let identity = NotificationIdentity(server: URL(string: "https://origin.example")!, token: "mdt_test",
                                            deviceID: "dev_test", bindingID: "bind_test")
        for choice in NotificationSoundChoice.allCases {
            MeowShared.notificationSound = choice
            for relay in [false, true] {
                let original = UNMutableNotificationContent()
                original.sound = NotificationSound.meow // Current servers request this sound.
                original.interruptionLevel = .timeSensitive
                original.userInfo = ["meow": ["id": "ntf_sound", "device_id": "dev_test",
                    "binding_id": "bind_test", "relay": relay]]
                let fallback = NotificationContent.fallback(original, identity: identity)
                let result = NotificationContent.forDisplay(fallback, identity: identity)
                XCTAssertEqual(result.sound, choice.sound, "\(choice) relay=\(relay)")
                XCTAssertEqual(result.interruptionLevel, .timeSensitive)
                XCTAssertEqual(original.sound, NotificationSound.meow, "display must not mutate the original content")
                if relay { XCTAssertEqual(result.categoryIdentifier, "") }
            }
        }
    }

    func testHydratedPriorityKeepsSilentMessagesSilentForEverySelection() throws {
        let identity = NotificationIdentity(server: URL(string: "https://origin.example")!, token: "mdt_test",
                                            deviceID: "dev_test", bindingID: "bind_test")
        for choice in NotificationSoundChoice.allCases {
            MeowShared.notificationSound = choice
            for priority in ["low", "normal", "high"] {
                let original = UNMutableNotificationContent()
                original.sound = NotificationSound.meow
                original.userInfo = ["meow": ["id": "ntf_sound", "binding_id": "bind_test", "relay": true]]
                let notification = MeowNotification(id: "ntf_sound", type: .info, title: "Sound test", body: "Body",
                    source: "Agent", priority: priority, pushStatus: "accepted", createdAt: .now)
                let hydrated = try XCTUnwrap(NotificationContent.hydrate(notification, original: original, identity: identity))
                let result = NotificationContent.forDisplay(hydrated, identity: identity)
                if priority == "low" {
                    XCTAssertNil(result.sound)
                    XCTAssertEqual(result.interruptionLevel, .passive)
                } else {
                    XCTAssertEqual(result.sound, choice.sound)
                    XCTAssertEqual(result.interruptionLevel, priority == "high" ? .timeSensitive : .active)
                }
            }
        }
    }

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
        for choice in NotificationSoundChoice.allCases {
            MeowShared.notificationSound = choice
            let result = NotificationContent.forDisplay(content, identity: nil)
            XCTAssertNil(result.sound)
            XCTAssertEqual(result.interruptionLevel, .passive)
        }
    }
}
