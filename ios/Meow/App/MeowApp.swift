import SwiftUI
import UIKit
import UserNotifications

@main
struct MeowApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(appDelegate.model)
        }
    }
}

@MainActor
final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    let model = AppModel()

    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        let center = UNUserNotificationCenter.current()
        center.delegate = self
        center.setNotificationCategories(NotificationCategory.all)
        model.didLaunch()
        return true
    }

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        model.didReceiveAPNsToken(deviceToken.map { String(format: "%02x", $0) }.joined())
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {
        model.pushRegistrationError = error.localizedDescription
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        NotificationCallback.run { () -> UNNotificationPresentationOptions in
            // An unreachable API must not hold up the system presentation.
            Task { await self.model.refresh() }
            return [.banner, .list, .sound, .badge]
        } completion: { completionHandler($0) }
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        NotificationCallback.run {
            await self.handle(response)
        } completion: { _ in completionHandler() }
    }

    private func handle(_ response: UNNotificationResponse) async {
        let meow = response.notification.request.content.userInfo["meow"] as? [String: Any]
        guard let meow, let identity = NotificationIdentity.current, identity.accepts(meow) else { return }
        // Ignore actions left on notifications delivered by a previous app version.
        // Opening a notification is the only interactive action in V1.
        guard response.actionIdentifier == UNNotificationDefaultActionIdentifier,
              let id = meow["id"] as? String else { return }
        model.open(id)
    }
}
