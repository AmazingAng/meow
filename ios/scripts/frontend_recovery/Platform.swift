// Native test boundaries only. The production app uses the actual iOS APIs.
import Foundation
import UserNotifications

enum RecoveryAuthorizationStatus { case notDetermined, denied, authorized, provisional, ephemeral }

enum UIDevice {
    static let current = Device()
    struct Device { let name = "Native recovery tests" }
}

enum UIApplication {
    static let shared = Application()
    struct Application { func registerForRemoteNotifications() {} }
}

final class RecoveryNotificationCenter: @unchecked Sendable {
    static let instance = RecoveryNotificationCenter()
    static func current() -> RecoveryNotificationCenter { instance }
    struct Settings {
        let authorizationStatus = RecoveryAuthorizationStatus.notDetermined
        let timeSensitiveSetting = UNNotificationSetting.notSupported
    }
    func setBadgeCount(_ value: Int) async throws {}
    func requestAuthorization(options: UNAuthorizationOptions) async throws -> Bool { false }
    func notificationSettings() async -> Settings { Settings() }
    func add(_ request: UNNotificationRequest) async throws {}
}

enum Keychain {
    private static let lock = NSLock()
    nonisolated(unsafe) private static var values: [String: String] = [:]
    static func get(_ key: String) -> String? { lock.withLock { values[key] } }
    @discardableResult
    static func set(_ value: String?, for key: String) -> Bool {
        lock.withLock { values[key] = value }
        return true
    }
}
