import Foundation
import Security

/// Configuration shared by the app and its notification service extension
/// through the App Group.
enum MeowShared {
    static let appGroup = "group.blue.meow.app"
    /// The server a fresh install suggests, from the app's MeowDefaultServer
    /// Info.plist key. Empty means there is none and onboarding asks for one.
    static let defaultServer = serverURL(from: Bundle.main.object(forInfoDictionaryKey: "MeowDefaultServer") as? String)

    static var defaults: UserDefaults { UserDefaults(suiteName: appGroup) ?? .standard }

    /// The saved server, or the default before one is chosen. Registering
    /// saves the server, so a registered device never depends on the default.
    static var server: URL? {
        get { defaults.string(forKey: "server").flatMap(URL.init(string:)) ?? defaultServer }
        set {
            if let newValue { defaults.set(newValue.absoluteString, forKey: "server") }
            else { defaults.removeObject(forKey: "server") }
        }
    }

    /// Parses a typed server address: http or https with a host. Surrounding
    /// spaces, trailing slashes, the query and the fragment are dropped.
    static func serverURL(from text: String?) -> URL? {
        let trimmed = (text ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        guard var c = URLComponents(string: trimmed),
              let scheme = c.scheme?.lowercased(), scheme == "https" || scheme == "http",
              let host = c.host, !host.isEmpty
        else { return nil }
        while c.path.hasSuffix("/") { c.path.removeLast() }
        c.query = nil
        c.fragment = nil
        return c.url
    }

    /// The mobile credential (mdt_…). It can answer questions, so it lives in the Keychain.
    static var deviceToken: String? {
        get { Keychain.get("device_token") }
        set { Keychain.set(newValue, for: "device_token") }
    }

    static var deviceID: String? {
        get { defaults.string(forKey: "device_id") }
        set { defaults.set(newValue, forKey: "device_id") }
    }

    static var gatewayBinding: GatewayBinding? {
        guard let text = Keychain.get("gateway_binding"), let data = text.data(using: .utf8) else { return nil }
        return try? JSONDecoder().decode(GatewayBinding.self, from: data)
    }

    @discardableResult
    static func saveGatewayBinding(_ binding: GatewayBinding?) -> Bool {
        guard let binding else { return Keychain.set(nil, for: "gateway_binding") }
        guard let data = try? JSONEncoder().encode(binding), let text = String(data: data, encoding: .utf8) else { return false }
        return Keychain.set(text, for: "gateway_binding")
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
