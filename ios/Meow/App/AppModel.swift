import SwiftUI
import UIKit
import UserNotifications

struct Toast: Identifiable, Equatable {
    let id = UUID()
    var message: String
}

@MainActor
@Observable
final class AppModel {
    enum Tab: Hashable { case inbox, settings }

    enum Filter: String, CaseIterable, Identifiable {
        case all, unread, pending
        var id: Self { self }
        var title: LocalizedStringKey {
            switch self {
            case .all: "All"
            case .unread: "Unread"
            case .pending: "Pending"
            }
        }
    }

    enum AnswerResult: Equatable {
        case sent
        case queued
        case refused(String)
    }

    // MARK: Registration

    private(set) var server = MeowShared.server
    private(set) var isRegistered = MeowShared.deviceToken != nil
    var hasFinishedOnboarding = MeowShared.defaults.bool(forKey: Keys.onboarded) {
        didSet { MeowShared.defaults.set(hasFinishedOnboarding, forKey: Keys.onboarded) }
    }
    private(set) var deviceKey = Keychain.get(Keys.deviceKey)
    private(set) var isRegistering = false
    private(set) var isChangingServer = false
    private(set) var permission = UNAuthorizationStatus.notDetermined
    private(set) var hasPushToken = false
    var pushRegistrationError: String?

    // MARK: Inbox

    private(set) var notifications: [MeowNotification] = []
    private(set) var counts = InboxCounts()
    private(set) var nextCursor: String?
    private(set) var isLoading = false
    private(set) var hasLoaded = false
    private(set) var loadError: String?
    var filter = Filter.all {
        didSet { if filter != oldValue { reload() } }
    }
    var search = "" {
        didSet { if search != oldValue { scheduleSearch() } }
    }
    /// Notifications opened from a push that may not be in the current page.
    private(set) var details: [String: MeowNotification] = [:]
    /// Answers saved on the device and not yet accepted, by question ID.
    private(set) var queuedReplies: [String: PendingReply] = [:]

    // MARK: Navigation and messages

    var selectedTab = Tab.inbox
    var inboxPath: [String] = []
    var toast: Toast?
    var alert: String?

    @ObservationIgnored private let replies: ReplyQueue
    @ObservationIgnored private var loadGeneration = 0
    @ObservationIgnored private var sessionGeneration = 0
    @ObservationIgnored private var isLoadingMore = false
    @ObservationIgnored private var searchTask: Task<Void, Never>?
    @ObservationIgnored private var retryTask: Task<Void, Never>?
    @ObservationIgnored private var apnsToken: String?

    private enum Keys {
        static let onboarded = "onboarded"
        static let deviceKey = "device_key"
        static let deviceKeyID = "device_key_id"
    }

    init(replies: ReplyQueue = .shared) {
        self.replies = replies
        #if DEBUG
        // Development helpers: -meowServer http://127.0.0.1:8080 -meowAutoRegister YES -meowOpen ntf_…
        if !isRegistered, let s = UserDefaults.standard.string(forKey: "meowServer"), let url = URL(string: s) {
            setServer(url)
        }
        #endif
    }

    private var api: MeowAPI { MeowAPI(server: server, token: MeowShared.deviceToken) }

    var serverDisplay: String {
        guard let host = server.host() else { return server.absoluteString }
        return server.port.map { "\(host):\($0)" } ?? host
    }

    var isNotificationPermissionGranted: Bool {
        [.authorized, .provisional, .ephemeral].contains(permission)
    }

    // MARK: Lifecycle

    func didLaunch() {
        Task {
            #if DEBUG
            if !isRegistered, UserDefaults.standard.bool(forKey: "meowAutoRegister") {
                await register()
                hasFinishedOnboarding = isRegistered
            }
            if let id = UserDefaults.standard.string(forKey: "meowOpen") { open(id) }
            #endif
            await refreshPermission()
        }
    }

    func scenePhaseChanged(_ phase: ScenePhase) {
        switch phase {
        case .active:
            Task {
                await refreshPermission()
                await flushReplies()
                await refresh()
            }
            startRetryLoop()
        case .background:
            retryTask?.cancel()
            retryTask = nil
        default:
            break
        }
    }

    // MARK: Registration

    func setServer(_ url: URL) {
        guard !isRegistered, !isRegistering, !isChangingServer else { return }
        server = url
        MeowShared.server = url
    }

    func register() async {
        guard !isRegistering, !isRegistered, !isChangingServer else { return }
        let registrationServer = server
        let generation = sessionGeneration
        isRegistering = true
        defer { isRegistering = false }
        do {
            let reg = try await MeowAPI(server: registrationServer, token: nil).register(name: UIDevice.current.name)
            guard generation == sessionGeneration, server == registrationServer else { return }
            MeowShared.deviceToken = reg.deviceToken
            // Without the token every later request fails with 401, so don't
            // continue as registered if the Keychain refused to store it.
            guard MeowShared.deviceToken == reg.deviceToken else {
                alert = String(localized: "Couldn't save this iPhone's credentials to the Keychain.")
                return
            }
            MeowShared.server = registrationServer
            store(key: reg.key)
            await replies.resume()
            isRegistered = true
            if let apnsToken { sendAPNsToken(apnsToken) }
        } catch {
            alert = Self.describe(error)
        }
    }

    /// Forgets this device and starts onboarding again, optionally on another server.
    @discardableResult
    func reset(server newServer: URL, discardPendingReplies: Bool = false) async -> Bool {
        guard !isRegistering, !isChangingServer else { return false }
        isChangingServer = true
        defer { isChangingServer = false }
        if discardPendingReplies {
            do { try await replies.discardAndPause() }
            catch {
                alert = String(localized: "Couldn't remove the saved replies. Your server and replies have been kept. Please try again. (\(error.localizedDescription))")
                return false
            }
        } else if !(await replies.pauseIfIdle()) {
            alert = String(localized: "You have replies waiting to send. Keep waiting, or discard them to change servers.")
            return false
        }
        sessionGeneration += 1
        loadGeneration += 1
        searchTask?.cancel()
        retryTask?.cancel()
        retryTask = nil
        isLoading = false
        isLoadingMore = false
        MeowShared.deviceToken = nil
        Keychain.set(nil, for: Keys.deviceKey)
        MeowShared.defaults.removeObject(forKey: Keys.deviceKeyID)
        deviceKey = nil
        isRegistered = false
        hasFinishedOnboarding = false
        hasPushToken = false
        notifications = []
        details = [:]
        counts = InboxCounts()
        nextCursor = nil
        hasLoaded = false
        loadError = nil
        inboxPath = []
        selectedTab = .inbox
        server = newServer
        MeowShared.server = newServer
        queuedReplies = [:]
        Task { try? await UNUserNotificationCenter.current().setBadgeCount(0) }
        return true
    }

    func pendingReplyCount() async -> Int { await replies.outstandingCount }

    func finishOnboarding() async {
        hasFinishedOnboarding = true
        await refresh()
    }

    private func store(key: MeowKey) {
        if let secret = key.secret {
            Keychain.set(secret, for: Keys.deviceKey)
            deviceKey = secret
        }
        MeowShared.defaults.set(key.id, forKey: Keys.deviceKeyID)
    }

    /// Creates a new Device Key and revokes the current one.
    func rotateKey() async {
        let generation = sessionGeneration
        let api = self.api
        do {
            let key = try await api.createKey(name: "Default", scopes: ["notify", "ask"])
            guard generation == sessionGeneration else { return }
            let old = MeowShared.defaults.string(forKey: Keys.deviceKeyID)
            store(key: key)
            if let old {
                do {
                    try await api.revokeKey(old)
                } catch {
                    alert = String(localized: "The new key works, but the old key could not be revoked: \(Self.describe(error))")
                    return
                }
            }
            toast = Toast(message: String(localized: "New Device Key created. Update it in your CLI."))
        } catch {
            alert = Self.describe(error)
        }
    }

    func loginCommand(for key: String) -> String {
        var cmd = "meow login --key \(key)"
        if server != MeowShared.defaultServer { cmd += " --server \(serverURLString)" }
        return cmd
    }

    func curlCommand(for key: String) -> String {
        "curl -H \"Authorization: Bearer \(key)\" -d \"Hello from curl\" \(serverURLString)/v1/notifications"
    }

    private var serverURLString: String {
        var s = server.absoluteString
        while s.hasSuffix("/") { s.removeLast() }
        return s
    }

    func sendTestNotification() async {
        guard let deviceKey else { return }
        let generation = sessionGeneration
        do {
            _ = try await MeowAPI(server: server, token: deviceKey).notify(
                title: String(localized: "Meow!"),
                body: String(localized: "Your agents can reach you now."))
            guard generation == sessionGeneration else { return }
            toast = Toast(message: isNotificationPermissionGranted
                ? String(localized: "Test notification sent.")
                : String(localized: "Sent. Notifications are off, so it's only in your inbox."))
            await refresh()
        } catch {
            alert = Self.describe(error)
        }
    }

    // MARK: Push

    func requestNotificationPermission() async {
        _ = try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge])
        await refreshPermission()
    }

    func refreshPermission() async {
        permission = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus
        if isNotificationPermissionGranted {
            UIApplication.shared.registerForRemoteNotifications()
        }
    }

    func didReceiveAPNsToken(_ token: String) {
        apnsToken = token
        if isRegistered { sendAPNsToken(token) }
    }

    private func sendAPNsToken(_ token: String) {
        #if DEBUG
        let environment = "sandbox" // builds run from Xcode use the APNs sandbox
        #else
        let environment = "production"
        #endif
        let api = self.api
        let generation = sessionGeneration
        Task {
            do {
                _ = try await api.updateDevice(apnsToken: token, apnsEnv: environment)
                guard generation == sessionGeneration else { return }
                hasPushToken = true
                pushRegistrationError = nil
            } catch {
                guard generation == sessionGeneration else { return }
                pushRegistrationError = Self.describe(error)
            }
        }
    }

    // MARK: Inbox

    func notification(id: String) -> MeowNotification? {
        notifications.first { $0.id == id } ?? details[id]
    }

    func refresh() async {
        guard isRegistered else { return }
        loadGeneration += 1
        let generation = loadGeneration
        let api = self.api
        let filter = self.filter.rawValue
        let search = self.search.trimmingCharacters(in: .whitespacesAndNewlines)
        isLoading = true
        defer { if generation == loadGeneration { isLoading = false } }
        do {
            async let page = api.inbox(filter: filter, search: search, cursor: nil)
            async let counts = api.counts()
            let (p, c) = try await (page, counts)
            guard generation == loadGeneration else { return }
            notifications = p.notifications
            nextCursor = p.nextCursor
            self.counts = c
            hasLoaded = true
            loadError = nil
            updateBadge()
        } catch {
            guard generation == loadGeneration, !Self.isCancellation(error) else { return }
            loadError = Self.describe(error)
            hasLoaded = true
        }
    }

    func loadMore() async {
        guard let cursor = nextCursor, !isLoadingMore else { return }
        isLoadingMore = true
        defer { isLoadingMore = false }
        let generation = loadGeneration
        do {
            let page = try await api.inbox(
                filter: filter.rawValue,
                search: search.trimmingCharacters(in: .whitespacesAndNewlines),
                cursor: cursor)
            guard generation == loadGeneration else { return }
            let known = Set(notifications.map(\.id))
            notifications += page.notifications.filter { !known.contains($0.id) }
            nextCursor = page.nextCursor
        } catch {
            guard generation == loadGeneration, !Self.isCancellation(error) else { return }
            loadError = Self.describe(error)
            nextCursor = nil // stop the spinner; pull to refresh starts over
        }
    }

    private func reload() {
        Task { await refresh() }
    }

    private func scheduleSearch() {
        searchTask?.cancel()
        searchTask = Task {
            try? await Task.sleep(for: .milliseconds(300))
            guard !Task.isCancelled else { return }
            await refresh()
        }
    }

    func refreshCounts() async {
        let generation = sessionGeneration
        guard isRegistered, let c = try? await api.counts(), generation == sessionGeneration else { return }
        counts = c
        updateBadge()
    }

    /// Fetches one notification; returns whether it is available to show.
    func loadDetail(_ id: String) async -> Bool {
        let generation = sessionGeneration
        do {
            let notification = try await api.notification(id)
            guard generation == sessionGeneration else { return false }
            upsert(notification)
            return true
        } catch {
            return notification(id: id) != nil
        }
    }

    func markRead(_ id: String) {
        guard var n = notification(id: id), n.isUnread else { return }
        n.readAt = .now
        upsert(n)
        counts.unread = max(0, counts.unread - 1)
        updateBadge()
        let api = self.api
        Task { try? await api.markRead(id) }
    }

    func markAllRead() async {
        let generation = sessionGeneration
        do {
            try await api.markAllRead()
            guard generation == sessionGeneration else { return }
            for i in notifications.indices where notifications[i].readAt == nil {
                notifications[i].readAt = .now
            }
            for (id, var n) in details where n.readAt == nil {
                n.readAt = .now
                details[id] = n
            }
            counts.unread = 0
            updateBadge()
            if filter == .unread { await refresh() }
        } catch {
            alert = Self.describe(error)
        }
    }

    /// Opens a notification, e.g. after the user taps a push.
    func open(_ id: String) {
        selectedTab = .inbox
        inboxPath = [id]
    }

    private func upsert(_ n: MeowNotification) {
        if let i = notifications.firstIndex(where: { $0.id == n.id }) {
            notifications[i] = n
        }
        details[n.id] = n
    }

    private func update(_ q: MeowQuestion) {
        for i in notifications.indices where notifications[i].question?.id == q.id {
            notifications[i].question?.apply(q)
            if notifications[i].readAt == nil { notifications[i].readAt = q.resolvedAt ?? .now }
        }
        if var n = details[q.notificationId] {
            n.question?.apply(q)
            if n.readAt == nil { n.readAt = q.resolvedAt ?? .now }
            details[n.id] = n
        }
    }

    private func updateBadge() {
        let unread = counts.unread
        Task { try? await UNUserNotificationCenter.current().setBadgeCount(unread) }
    }

    // MARK: Answers

    func answer(_ questionID: String, with answer: String) async -> AnswerResult {
        guard isRegistered, !isChangingServer else {
            return .refused(String(localized: "The server is changing. Please try again afterward."))
        }
        let generation = sessionGeneration
        let outcome = await replies.submit(questionID: questionID, answer: answer)
        guard generation == sessionGeneration, !isChangingServer else {
            return .refused(String(localized: "The server has changed."))
        }
        let result = apply(outcome, to: questionID)
        await refreshCounts()
        return result
    }

    /// Answers from a notification action. The user isn't looking at the app,
    /// so a refusal is reported with a local notification.
    func answerFromNotification(_ questionID: String, with answer: String) async {
        guard case .refused(let reason) = await self.answer(questionID, with: answer) else { return }
        let content = UNMutableNotificationContent()
        content.title = String(localized: "Your answer wasn't accepted")
        content.body = reason
        content.categoryIdentifier = NotificationCategory.info
        try? await UNUserNotificationCenter.current().add(
            UNNotificationRequest(identifier: "refused-\(questionID)", content: content, trigger: nil))
    }

    func flushReplies() async {
        guard isRegistered, !isChangingServer else { return }
        let generation = sessionGeneration
        let results = await replies.flush()
        let pending = await replies.pending
        guard generation == sessionGeneration, !isChangingServer else { return }
        for (reply, outcome) in results { apply(outcome, to: reply.questionID) }
        queuedReplies = Dictionary(pending.map { ($0.questionID, $0) }, uniquingKeysWith: { first, _ in first })
    }

    @discardableResult
    private func apply(_ outcome: ReplyOutcome, to questionID: String) -> AnswerResult {
        switch outcome {
        case .discarded:
            return .refused(String(localized: "This saved reply was discarded when changing servers."))
        case .notSaved(let reason):
            return .refused(reason)
        case .accepted(let q):
            queuedReplies[questionID] = nil
            update(q)
            return .sent
        case .refused(let error):
            queuedReplies[questionID] = nil
            if let q = error.question { update(q) }
            return .refused(Self.describeRefusal(error))
        case .queued(let reply):
            queuedReplies[questionID] = reply
            startRetryLoop()
            return .queued
        case .alreadyQueued(let reply):
            queuedReplies[questionID] = reply
            return .refused(String(localized: "Already sending “\(reply.answer)”."))
        }
    }

    private func startRetryLoop() {
        guard retryTask == nil else { return }
        retryTask = Task { [weak self] in
            while !Task.isCancelled {
                do { try await Task.sleep(for: .seconds(15)) } catch { return }
                guard let self else { return }
                if !self.queuedReplies.isEmpty { await self.flushReplies() }
            }
        }
    }

    // MARK: Errors

    private static func isCancellation(_ error: Error) -> Bool {
        error is CancellationError || (error as? URLError)?.code == .cancelled
    }

    static func describe(_ error: Error) -> String {
        if let e = error as? MeowAPIError {
            if e.status == 401 {
                return String(localized: "This iPhone is no longer registered with the server. Change or reset the server in Settings.")
            }
            return e.localizedDescription
        }
        if let e = error as? URLError {
            switch e.code {
            case .notConnectedToInternet, .networkConnectionLost, .dataNotAllowed:
                return String(localized: "You're offline.")
            case .cannotConnectToHost, .cannotFindHost, .timedOut, .dnsLookupFailed:
                return String(localized: "Can't reach the Meow server.")
            default:
                break
            }
        }
        return error.localizedDescription
    }

    static func describeRefusal(_ error: MeowAPIError) -> String {
        switch error.code {
        case "already_resolved":
            if let answer = error.question?.answer {
                return String(localized: "This question was already answered: “\(answer)”.")
            }
            return String(localized: "This question is no longer waiting for an answer.")
        case "expired":
            return String(localized: "This question expired before your answer arrived.")
        case "invalid_answer":
            return String(localized: "That answer isn't one of the choices.")
        case "not_found":
            return String(localized: "This question no longer exists.")
        default:
            return describe(error)
        }
    }
}
