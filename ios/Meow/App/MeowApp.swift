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

/// Notification categories and actions. The category identifiers match the
/// ones the server puts in each push (server/internal/push/payload.go).
enum NotificationCategory {
    static let info = "MEOW_INFO"
    static let reply = "MEOW_REPLY"
    static let approval = "MEOW_APPROVAL"
    static let choice = "MEOW_CHOICE" // custom choices are answered in the app

    static let approveAction = "APPROVE"
    static let rejectAction = "REJECT"
    static let replyAction = "REPLY"

    static var all: Set<UNNotificationCategory> {
        let approveButton = UNNotificationAction(
            identifier: approveAction, title: String(localized: "Approve"),
            // The phone must be unlocked to approve, so no one can approve from a locked screen.
            options: [.authenticationRequired],
            icon: UNNotificationActionIcon(systemImageName: "checkmark.circle"))
        let rejectButton = UNNotificationAction(
            identifier: rejectAction, title: String(localized: "Reject"),
            options: [.destructive],
            icon: UNNotificationActionIcon(systemImageName: "xmark.circle"))
        let replyField = UNTextInputNotificationAction(
            identifier: replyAction, title: String(localized: "Reply"), options: [],
            icon: UNNotificationActionIcon(systemImageName: "arrowshape.turn.up.left"),
            textInputButtonTitle: String(localized: "Send"),
            textInputPlaceholder: String(localized: "Reply to the agent"))
        return [
            UNNotificationCategory(identifier: info, actions: [], intentIdentifiers: []),
            UNNotificationCategory(identifier: reply, actions: [replyField], intentIdentifiers: []),
            UNNotificationCategory(identifier: approval, actions: [approveButton, rejectButton], intentIdentifiers: []),
            UNNotificationCategory(identifier: choice, actions: [], intentIdentifiers: []),
        ]
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
        willPresent notification: UNNotification
    ) async -> UNNotificationPresentationOptions {
        await model.refresh()
        return [.banner, .list, .sound, .badge]
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse
    ) async {
        let meow = response.notification.request.content.userInfo["meow"] as? [String: Any]
        await handle(
            action: response.actionIdentifier,
            notificationID: meow?["id"] as? String,
            questionID: meow?["question_id"] as? String,
            choices: meow?["choices"] as? [String] ?? [],
            text: (response as? UNTextInputNotificationResponse)?.userText)
    }

    private func handle(action: String, notificationID: String?, questionID: String?, choices: [String], text: String?) async {
        // Send the question's own spelling of the choice.
        func choice(_ name: String) -> String {
            choices.first { $0.lowercased() == name } ?? name
        }
        switch action {
        case NotificationCategory.approveAction:
            if let questionID { await model.answerFromNotification(questionID, with: choice("approve")) }
        case NotificationCategory.rejectAction:
            if let questionID { await model.answerFromNotification(questionID, with: choice("reject")) }
        case NotificationCategory.replyAction:
            let reply = text?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            if let questionID, !reply.isEmpty { await model.answerFromNotification(questionID, with: reply) }
        case UNNotificationDefaultActionIdentifier:
            if let notificationID { model.open(notificationID) }
        default:
            break
        }
    }
}
