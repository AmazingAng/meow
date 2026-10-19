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
        let isDefault = response.actionIdentifier == UNNotificationDefaultActionIdentifier
        guard isDefault || NotificationContent.canRespond(meow, identity: identity) else { return }
        await handle(
            action: response.actionIdentifier,
            notificationID: meow["id"] as? String,
            questionID: meow["question_id"] as? String,
            choices: meow["choices"] as? [String] ?? [],
            text: (response as? UNTextInputNotificationResponse)?.userText,
            identity: identity)
    }

    private func handle(action: String, notificationID: String?, questionID: String?, choices: [String], text: String?, identity: NotificationIdentity) async {
        guard NotificationIdentity.current == identity else { return }
        // Send the question's own spelling of the choice.
        func choice(_ name: String) -> String {
            choices.first { $0.lowercased() == name } ?? name
        }
        switch action {
        case NotificationCategory.approveAction:
            if let questionID { await model.answerFromNotification(questionID, with: choice("approve"), identity: identity) }
        case NotificationCategory.rejectAction:
            if let questionID { await model.answerFromNotification(questionID, with: choice("reject"), identity: identity) }
        case NotificationCategory.replyAction:
            let reply = text?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            if let questionID, !reply.isEmpty { await model.answerFromNotification(questionID, with: reply, identity: identity) }
        case UNNotificationDefaultActionIdentifier:
            if let notificationID { model.open(notificationID) }
        default:
            break
        }
    }
}
