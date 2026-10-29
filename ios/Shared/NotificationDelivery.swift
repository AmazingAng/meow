import Foundation
import UserNotifications

/// The extension's lifecycle, separated from the OS entry point so fetch,
/// timeout and identity-change races can be exercised deterministically.
final class NotificationDelivery: @unchecked Sendable {
    typealias Fetch = @Sendable (String, NotificationIdentity) async throws -> MeowNotification
    typealias Report = @Sendable (String, NotificationIdentity) async throws -> Void
    typealias Record = @Sendable (MeowNotification, NotificationIdentity) async throws -> Void
    typealias Counts = @Sendable (NotificationIdentity) async throws -> Int

    private let lock = NSLock()
    private let original: UNNotificationContent
    private let identity: NotificationIdentity?
    private let currentIdentity: @Sendable () -> NotificationIdentity?
    private let fetch: Fetch
    private let report: Report
    private let record: Record
    private let counts: Counts
    private var handler: ((UNNotificationContent) -> Void)?
    private var content: UNNotificationContent
    private var task: Task<Void, Never>?
    private var auxiliaryTasks: [Task<Void, Never>] = []
    private var auxiliaryRemaining = 0
    private var recorded = false
    private var started = false

    init(original: UNNotificationContent,
         currentIdentity: @escaping @Sendable () -> NotificationIdentity? = { NotificationIdentity.current },
         fetch: @escaping Fetch, report: @escaping Report,
         record: @escaping Record = { _, _ in },
         counts: @escaping Counts = { _ in throw URLError(.unsupportedURL) },
         handler: @escaping (UNNotificationContent) -> Void) {
        self.original = original
        self.currentIdentity = currentIdentity
        self.identity = currentIdentity()
        self.fetch = fetch
        self.report = report
        self.record = record
        self.counts = counts
        self.handler = handler
        let fallback = NotificationContent.fallback(original, identity: identity)
        fallback.badge = nil
        self.content = fallback
    }

    func start() {
        guard lock.withLock({ if started || handler == nil { return false }; started = true; return true }) else { return }
        guard let m = original.userInfo["meow"] as? [String: Any], let identity,
              identity.accepts(m), let id = m["id"] as? String else { finish(); return }
        let relay = m["relay"] as? Bool == true
        // Direct pushes already have a safe preview; origin work must not delay
        // it beyond the same auxiliary budget when the origin is unavailable.
        if !relay {
            let deadline = Task { [weak self] in
                do { try await Task.sleep(for: .milliseconds(250)) } catch { return }
                self?.finish()
            }
            lock.withLock { auxiliaryTasks.append(deadline) }
        }
        let running = Task { [weak self] in
            guard let self else { return }
            do {
                let n = try await fetch(id, identity)
                guard isActive, let hydrated = NotificationContent.hydrate(n, original: original, identity: identity) else { finish(); return }
                hydrated.badge = nil
                lock.withLock { if self.handler != nil { self.content = hydrated } }
                guard isActive else { finish(); return }
                startAuxiliaries(notification: n, identity: identity)
            } catch { finish() }
        }
        let retained = lock.withLock {
            guard handler != nil else { return false }
            task = running
            return true
        }
        if !retained { running.cancel() }
    }

    // Metadata is best-effort. One shared deadline bounds waiting even if a
    // network boundary ignores cancellation; verified text is already ready.
    private func startAuxiliaries(notification: MeowNotification, identity: NotificationIdentity) {
        guard lock.withLock({
            guard handler != nil else { return false }
            auxiliaryRemaining = 2
            return true
        }) else { return }
        var tasks: [Task<Void, Never>] = []
        tasks.append(Task { [weak self] in
            do { try await Task.sleep(for: .milliseconds(250)) } catch { return }
            self?.finish()
        })
        tasks.append(Task { [weak self] in
            guard let self else { return }
            do {
                guard isActive else { return }
                try await record(notification, identity)
                lock.withLock { self.recorded = true }
            } catch { /* verified text remains available; the existing badge stays */ }
            auxiliaryFinished()
        })
        tasks.append(Task { [weak self] in
            guard let self else { return }
            if isActive { try? await report(notification.id, identity) }
            auxiliaryFinished()
        })
        let retained = lock.withLock {
            guard handler != nil else { return false }
            auxiliaryTasks += tasks
            return true
        }
        if !retained { tasks.forEach { $0.cancel() } }
    }

    private func auxiliaryFinished() {
        let done = lock.withLock {
            guard handler != nil, auxiliaryRemaining > 0 else { return false }
            auxiliaryRemaining -= 1
            return auxiliaryRemaining == 0
        }
        if done {
            let shouldCount = lock.withLock { handler != nil && recorded }
            guard shouldCount else { finish(); return }
            // Read the current local count only at the end, so a read performed
            // while receipt reporting was in flight cannot restore an old badge.
            let latest = Task { [weak self] in
                guard let self else { return }
                if let identity, let unread = try? await counts(identity), isActive {
                    lock.withLock {
                        if self.handler != nil, let updated = self.content.mutableCopy() as? UNMutableNotificationContent {
                            updated.badge = NSNumber(value: unread); self.content = updated
                        }
                    }
                }
                finish()
            }
            let retained = lock.withLock {
                guard handler != nil else { return false }
                auxiliaryTasks.append(latest); return true
            }
            if !retained { latest.cancel() }
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
        let pending: (((UNNotificationContent) -> Void)?, UNNotificationContent, [Task<Void, Never>]) = lock.withLock {
            defer { handler = nil; task = nil; auxiliaryTasks = []; auxiliaryRemaining = 0; recorded = false }
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
            return (handler, content, auxiliaryTasks + [task].compactMap { $0 })
        }
        pending.2.forEach { $0.cancel() }
        pending.0?(NotificationContent.forDisplay(pending.1, identity: identity))
    }
}
