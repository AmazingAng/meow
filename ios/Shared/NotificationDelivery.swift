import Foundation
import UserNotifications

/// The extension's lifecycle, separated from the OS entry point so fetch,
/// timeout and identity-change races can be exercised deterministically.
final class NotificationDelivery: @unchecked Sendable {
    typealias Fetch = @Sendable (String, NotificationIdentity) async throws -> MeowNotification
    typealias Report = @Sendable (String, NotificationIdentity) async throws -> Void
    typealias Counts = @Sendable (NotificationIdentity) async throws -> Int

    private let lock = NSLock()
    private let original: UNNotificationContent
    private let identity: NotificationIdentity?
    private let currentIdentity: @Sendable () -> NotificationIdentity?
    private let fetch: Fetch
    private let report: Report
    private let counts: Counts
    private var handler: ((UNNotificationContent) -> Void)?
    private var content: UNNotificationContent
    private var task: Task<Void, Never>?

    init(original: UNNotificationContent,
         currentIdentity: @escaping @Sendable () -> NotificationIdentity? = { NotificationIdentity.current },
         fetch: @escaping Fetch, report: @escaping Report,
         counts: @escaping Counts = { _ in throw URLError(.unsupportedURL) },
         handler: @escaping (UNNotificationContent) -> Void) {
        self.original = original
        self.currentIdentity = currentIdentity
        self.identity = currentIdentity()
        self.fetch = fetch
        self.report = report
        self.counts = counts
        self.handler = handler
        self.content = NotificationContent.fallback(original, identity: identity)
    }

    func start() {
        guard let m = original.userInfo["meow"] as? [String: Any], let identity,
              identity.accepts(m), let id = m["id"] as? String else { finish(); return }
        let relay = m["relay"] as? Bool == true
        let running = Task { [weak self] in
            guard let self else { return }
            do {
                if relay {
                    let n = try await fetch(id, identity)
                    guard isActive, let hydrated = NotificationContent.hydrate(n, original: original, identity: identity) else { finish(); return }
                    lock.withLock { if self.handler != nil { self.content = hydrated } }
                    if let unread = try? await counts(identity), isActive {
                        lock.withLock {
                            if self.handler != nil, let updated = self.content.mutableCopy() as? UNMutableNotificationContent {
                                updated.badge = NSNumber(value: unread)
                                self.content = updated
                            }
                        }
                    }
                }
                if isActive { try? await report(id, identity) }
            } catch { /* no content was verified: retain the generic fallback */ }
            finish()
        }
        lock.withLock {
            if handler != nil { task = running } else { running.cancel() }
        }
    }

    private var isActive: Bool {
        !Task.isCancelled && currentIdentity() == identity && lock.withLock { handler != nil }
    }

    func expire() {
        let running = lock.withLock { task }
        running?.cancel()
        finish()
    }

    private func finish() {
        let pending: (((UNNotificationContent) -> Void)?, UNNotificationContent) = lock.withLock {
            defer { handler = nil }
            if currentIdentity() != identity {
                let stale = original.mutableCopy() as! UNMutableNotificationContent
                stale.title = "Meow"
                stale.body = String(localized: "New message. Open Meow to view.")
                stale.subtitle = ""
                stale.threadIdentifier = ""
                stale.categoryIdentifier = ""
                stale.userInfo = [:]
                stale.badge = nil
                content = stale
            }
            return (handler, content)
        }
        pending.0?(NotificationContent.forDisplay(pending.1, identity: identity))
    }
}
