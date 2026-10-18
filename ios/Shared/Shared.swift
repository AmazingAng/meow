import Foundation
import Security

/// Configuration shared by the app and its notification service extension
/// through the App Group.
enum MeowShared {
    static let appGroup = "group.blue.meow.app"
    static let defaultServer = URL(string: "https://api.meow.blue")!

    static var defaults: UserDefaults { UserDefaults(suiteName: appGroup) ?? .standard }

    static var server: URL {
        get { defaults.string(forKey: "server").flatMap(URL.init(string:)) ?? defaultServer }
        set { defaults.set(newValue.absoluteString, forKey: "server") }
    }

    /// The mobile credential (mdt_…). It can answer questions, so it lives in the Keychain.
    static var deviceToken: String? {
        get { Keychain.get("device_token") }
        set { Keychain.set(newValue, for: "device_token") }
    }

    static var containerURL: URL {
        FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroup)
            ?? FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
    }
}

/// Generic-password Keychain items shared through the App Group.
enum Keychain {
    private static let service = "blue.meow"

    // Unsigned simulator builds may lack the App Group entitlement; fall back
    // to the app's own keychain then.
    nonisolated(unsafe) private static var useGroup = true

    private static func query(_ account: String) -> [String: Any] {
        var q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        if useGroup { q[kSecAttrAccessGroup as String] = MeowShared.appGroup }
        return q
    }

    static func get(_ account: String) -> String? {
        var q = query(account)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecMissingEntitlement, useGroup {
            useGroup = false
            return get(account)
        }
        guard status == errSecSuccess, let data = out as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    @discardableResult
    static func set(_ value: String?, for account: String) -> Bool {
        let q = query(account)
        var status: OSStatus
        if let value {
            // Readable after first unlock so the extension can report delivery on a locked phone.
            let attrs: [String: Any] = [
                kSecValueData as String: Data(value.utf8),
                kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
            ]
            status = SecItemUpdate(q as CFDictionary, attrs as CFDictionary)
            if status == errSecItemNotFound {
                status = SecItemAdd(q.merging(attrs) { $1 } as CFDictionary, nil)
            }
        } else {
            status = SecItemDelete(q as CFDictionary)
            if status == errSecItemNotFound { status = errSecSuccess }
        }
        if status == errSecMissingEntitlement, useGroup {
            useGroup = false
            return set(value, for: account)
        }
        return status == errSecSuccess
    }
}
