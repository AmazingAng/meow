import UserNotifications

/// Reports to the server that a push reached the device, so "accepted by APNs"
/// and "delivered" stay distinct. The notification is shown unchanged.
final class NotificationService: UNNotificationServiceExtension {
    private let lock = NSLock()
    private var contentHandler: ((UNNotificationContent) -> Void)?
    private var content: UNNotificationContent?
    private var task: Task<Void, Never>?

    override func didReceive(
        _ request: UNNotificationRequest,
        withContentHandler contentHandler: @escaping (UNNotificationContent) -> Void
    ) {
        lock.withLock {
            self.contentHandler = contentHandler
            self.content = request.content
        }
        guard let meow = request.content.userInfo["meow"] as? [String: Any],
              let id = meow["id"] as? String,
              let token = MeowShared.deviceToken,
              let server = MeowShared.server
        else {
            finish()
            return
        }
        let api = MeowAPI(server: server, token: token)
        task = Task { [weak self] in
            try? await api.markDelivered(id)
            self?.finish()
        }
    }

    override func serviceExtensionTimeWillExpire() {
        task?.cancel()
        finish()
    }

    /// Hands the content to the system exactly once.
    private func finish() {
        let pending: (((UNNotificationContent) -> Void)?, UNNotificationContent?) = lock.withLock {
            defer { contentHandler = nil }
            return (contentHandler, content)
        }
        if let handler = pending.0, let content = pending.1 {
            handler(content)
        }
    }
}
