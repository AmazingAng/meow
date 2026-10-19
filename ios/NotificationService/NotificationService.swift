import UserNotifications

/// A visible, mutable APNs alert provides the safe fallback if iOS cannot run
/// the extension. Message content and response actions are filled by the phone.
final class NotificationService: UNNotificationServiceExtension {
    private var delivery: NotificationDelivery?

    override func didReceive(
        _ request: UNNotificationRequest,
        withContentHandler contentHandler: @escaping (UNNotificationContent) -> Void
    ) {
        let delivery = NotificationDelivery(original: request.content, fetch: { id, identity in
            try await Self.api(identity).notification(id, timeout: 12)
        }, report: { id, identity in
            try await Self.api(identity).markDelivered(id, timeout: 3)
        }, counts: { identity in
            try await Self.api(identity).counts(timeout: 3).unread
        }, handler: contentHandler)
        self.delivery = delivery
        delivery.start()
    }

    override func serviceExtensionTimeWillExpire() { delivery?.expire() }

    private static func api(_ identity: NotificationIdentity) -> MeowAPI {
        MeowAPI(server: identity.server, token: identity.token, session: MeowTransport.gateway)
    }
}
