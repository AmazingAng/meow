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
        }, record: { item, identity in
            let scope = InboxStore.scope(server: identity.server, token: identity.token)
            try await InboxStore.notificationStore.receiveVerified(item, scope: scope) { NotificationIdentity.current == identity }
        }, counts: { identity in
            let scope = InboxStore.scope(server: identity.server, token: identity.token)
            return try await InboxStore.notificationStore.unreadCount(scope: scope)
        }, handler: contentHandler)
        self.delivery = delivery
        delivery.start()
    }

    override func serviceExtensionTimeWillExpire() { delivery?.expire() }

    private static func api(_ identity: NotificationIdentity) -> MeowAPI {
        MeowAPI(server: identity.server, token: identity.token, session: MeowTransport.gateway)
    }
}
