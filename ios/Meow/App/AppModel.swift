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
        static let visibleCases: [Self] = [.all, .unread]
        var id: Self { self }
        var title: LocalizedStringKey {
            switch self {
            case .all: "All"
            case .unread: "Unread"
            case .pending: "Needs attention"
            }
        }
    }

    /// Why an address can't be used as the Meow server.
    enum ServerProblem: Error, Equatable {
        case invalidAddress
        case insecure
        case offline
        case untrusted(host: String)
        case unreachable(host: String)
        case notMeow(host: String)

        var message: String {
            switch self {
            case .invalidAddress:
                String(localized: "Enter a full address such as https://meow.example.com.")
            case .insecure:
                String(localized: "Use an https:// address.")
            case .offline:
                String(localized: "You're offline.")
            case .untrusted(let host):
                String(localized: "The TLS certificate of \(host) isn't trusted. Use a certificate from a public CA.")
            case .unreachable(let host):
                String(localized: "Can't reach \(host). Check the address and that the server is running.")
            case .notMeow(let host):
                String(localized: "\(host) doesn't look like a Meow server. Check the address.")
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
    private(set) var isRegistered = MeowShared.deviceToken != nil && MeowShared.server != nil
    var hasFinishedOnboarding = MeowShared.defaults.bool(forKey: Keys.onboarded) {
        didSet { MeowShared.defaults.set(hasFinishedOnboarding, forKey: Keys.onboarded) }
    }
    private(set) var deviceKey = Keychain.get(Keys.deviceKey)
    private(set) var isRegistering = false
    private(set) var isChangingServer = false
    private(set) var permission = UNAuthorizationStatus.notDetermined
    private(set) var timeSensitiveSetting = UNNotificationSetting.notSupported
    /// Notifications this iPhone sent to itself, which say nothing about the computer.
    private(set) var sentTestIDs: Set<String> = []
    private var sendingTestCount = 0

    /// Any computer message in this device's inbox proves the connection,
    /// including one received before the test page appears or the app restarts.
    var onboardingNotification: MeowNotification? {
        guard isRegistered, !isChangingServer, sendingTestCount == 0 else { return nil }
        return savedNotifications.first { !sentTestIDs.contains($0.id) }
    }
    private(set) var hasPushToken = false
    var pushRegistrationError: String?
    private(set) var gatewayURL: URL?
    private(set) var isConfiguringPush = false
    @ObservationIgnored private var pushTask: Task<Void, Never>?


    // MARK: Inbox

    private(set) var savedNotifications: [MeowNotification] = [] { didSet { rebuildInboxGroups() } }
    private var displayLimit = 50
    var notifications: [MeowNotification] {
        let matches = matchingNotifications
        if filter == .all, search.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            let groups = InboxPresentation(matches, now: .now)
            return groups.pending + Array(groups.activity.prefix(displayLimit))
        }
        return filter == .pending ? InboxPresentation(matches, now: .now).pending : Array(matches.prefix(displayLimit))
    }
    private(set) var counts = InboxCounts()
    var nextCursor: String? {
        let matches = matchingNotifications
        let count = filter == .all && search.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            ? InboxPresentation(matches, now: .now).activity.count : matches.count
        return filter != .pending && count > displayLimit ? "local-\(displayLimit)" : nil
    }
    private(set) var isLoading = false
    private(set) var isDeletingNotifications = false
    private(set) var hasLoaded = false
    private(set) var loadError: String?
    private(set) var inboxStorageError: String?
    var filter = Filter.all {
        didSet { if filter != oldValue { displayLimit = 50; groupDisplayLimit = 50; rebuildInboxGroups() } }
    }
    var search = "" {
        didSet { if search != oldValue { displayLimit = 50; groupDisplayLimit = 50; rebuildInboxGroups() } }
    }
    var inboxMode = InboxGroupingMode.messages {
        didSet {
            guard inboxMode != oldValue else { return }
            groupingStore.mode = inboxMode
            groupDisplayLimit = 50
            inboxPath = []
            allInboxGroups = []
            rebuildInboxGroups()
        }
    }
    private(set) var allInboxGroups: [InboxGroup] = []
    private(set) var isGroupingInbox = false
    private var groupDisplayLimit = 50
    var visibleInboxGroups: [InboxGroup] { Array(matchingInboxGroups.prefix(groupDisplayLimit)) }
    var matchingInboxGroups: [InboxGroup] { allInboxGroups.filter { !$0.matches.isEmpty } }
    var hasMoreInboxGroups: Bool { matchingInboxGroups.count > groupDisplayLimit }
    var inboxGroupingScope: String? { inboxScope }
    private(set) var isMarkingGroupRead = false
    private(set) var isMarkingAllRead = false
    private(set) var failedGroupReadIDs: Set<String> = []
    @ObservationIgnored private var groupingTask: Task<Void, Never>?
    @ObservationIgnored private var groupingRevision = 0
    @ObservationIgnored private let groupingStore: InboxGroupingStore

    /// Notifications opened from a push that may not be in the current page.
    private(set) var details: [String: MeowNotification] = [:]
    /// Answers saved on the device and not yet accepted, by question ID.
    private(set) var queuedReplies: [String: PendingReply] = [:]
    private(set) var failedReplies: [PendingReply] = []

    // MARK: Navigation and messages

    var selectedTab = Tab.inbox
    var inboxPath: [String] = []
    var toast: Toast?
    var alert: String?

    @ObservationIgnored private let replies: ReplyQueue
    @ObservationIgnored private let pushSession: URLSession
    @ObservationIgnored private let inboxStore: InboxStore
    @ObservationIgnored private var loadGeneration = 0
    @ObservationIgnored private var sessionGeneration = 0
    @ObservationIgnored private var hydrationTask: Task<[MeowNotification]?, Error>?
    @ObservationIgnored private var hydratedScope: String?
    @ObservationIgnored private var mutationRevision: UInt64 = 0
    @ObservationIgnored private var persistenceRevision: UInt64 = 0
    @ObservationIgnored private var itemRevisions: [String: UInt64] = [:]
    @ObservationIgnored private var retryTask: Task<Void, Never>?
    @ObservationIgnored private var apnsToken: String?
    @ObservationIgnored private var readRequests: [String: UUID] = [:]
    @ObservationIgnored private var countsRevision = 0
    @ObservationIgnored private var deletionRevision = 0
    // Server cache membership is separate from the iPhone's durable history.
    @ObservationIgnored private var serverNotificationIDs: Set<String> = []
    @ObservationIgnored private var serverUnreadIDs: Set<String> = []
    @ObservationIgnored private var serverPendingIDs: Set<String> = []
    @ObservationIgnored private var serverCounts: InboxCounts?

    private enum Keys {
        static let onboarded = "onboarded"
        static let deviceKey = "device_key"
        static let deviceKeyID = "device_key_id"
        static let sentTests = "onboarding_sent_tests"
    }

    init(replies: ReplyQueue = .shared, pushSession: URLSession = MeowTransport.gateway,
         inboxStore: InboxStore = .shared, groupingStore: InboxGroupingStore = InboxGroupingStore()) {
        self.replies = replies
        self.pushSession = pushSession
        self.inboxStore = inboxStore
        self.groupingStore = groupingStore
        self.inboxMode = groupingStore.mode
        if let scope = inboxScope,
           let saved = MeowShared.defaults.dictionary(forKey: Keys.sentTests),
           saved["scope"] as? String == scope,
           let ids = saved["ids"] as? [String] {
            sentTestIDs = Set(ids)
        }
        #if DEBUG
        // Development helpers: -meowServer http://127.0.0.1:8080 -meowAutoRegister YES -meowOpen ntf_…
        if !isRegistered, let url = MeowShared.serverURL(from: UserDefaults.standard.string(forKey: "meowServer")) {
            setServer(url)
        }
        #endif
    }

    private var api: MeowAPI? {
        guard let server, let token = MeowShared.deviceToken else { return nil }
        return MeowAPI(server: server, token: token)
    }

    var serverDisplay: String {
        guard let server else { return "" }
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
            if let token = UserDefaults.standard.string(forKey: "meowAPNsToken") { didReceiveAPNsToken(token) }
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
                await loadSavedReplyRecords()
                await refresh()
            }
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
        guard !isRegistering, !isRegistered, !isChangingServer, let registrationServer = server else { return }
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
            MeowShared.deviceID = reg.device.id
            store(key: reg.key)
            await replies.resume()
            isRegistered = true
            rebuildInboxGroups()
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
            if await replies.outstandingCount > 0 {
                alert = String(localized: "You have replies waiting to send. Keep waiting, or discard them to change servers.")
            } else {
                alert = String(localized: "Couldn't prepare saved reply records. Your server and records have been kept. Check available storage and try again.")
            }
            return false
        }
        if let scope = inboxScope {
            do { try await inboxStore.erase(scope: scope); groupingStore.erase(scope: scope) }
            catch {
                await replies.resume()
                alert = String(localized: "Couldn't remove saved messages. Your server has been kept. (\(error.localizedDescription))")
                return false
            }
        }
        let previousGateway = MeowShared.gatewayBinding
        pushTask?.cancel()
        pushTask = nil
        MeowShared.saveGatewayBinding(nil)
        MeowShared.deviceID = nil
        gatewayURL = nil
        isConfiguringPush = false
        if let previousGateway { Self.revokeGateway(previousGateway, session: pushSession) }
        sessionGeneration += 1
        loadGeneration += 1
        hydrationTask?.cancel()
        hydrationTask = nil
        hydratedScope = nil
        retryTask?.cancel()
        retryTask = nil
        isLoading = false
        isDeletingNotifications = false
        deletionRevision += 1
        MeowShared.deviceToken = nil
        Keychain.set(nil, for: Keys.deviceKey)
        MeowShared.defaults.removeObject(forKey: Keys.deviceKeyID)
        deviceKey = nil
        isRegistered = false
        hasFinishedOnboarding = false
        hasPushToken = false
        sentTestIDs = []
        sendingTestCount = 0
        MeowShared.defaults.removeObject(forKey: Keys.sentTests)
        groupingTask?.cancel()
        groupingRevision += 1
        allInboxGroups = []
        isGroupingInbox = false
        isMarkingGroupRead = false
        isMarkingAllRead = false
        failedGroupReadIDs = []
        savedNotifications = []
        serverNotificationIDs = []
        serverUnreadIDs = []
        serverPendingIDs = []
        serverCounts = nil
        details = [:]
        counts = InboxCounts()
        displayLimit = 50
        groupDisplayLimit = 50
        filter = .all
        search = ""
        mutationRevision = 0
        itemRevisions = [:]
        hasLoaded = false
        loadError = nil
        inboxStorageError = nil
        inboxPath = []
        selectedTab = .inbox
        server = newServer
        MeowShared.server = newServer
        queuedReplies = [:]
        failedReplies = []
        readRequests = [:]
        countsRevision += 1
        Task { try? await UNUserNotificationCenter.current().setBadgeCount(0) }
        return true
    }

    func pendingReplyCount() async -> Int { await replies.outstandingCount }

    /// Parses `text` and asks GET /healthz, so a typo or the wrong server is
    /// caught before registering.
    static func checkServer(_ text: String) async -> Result<URL, ServerProblem> {
        guard let url = MeowShared.serverURL(from: text) else { return .failure(.invalidAddress) }
        let host = url.host() ?? url.absoluteString
        do {
            return try await MeowAPI(server: url, token: nil).isHealthy() ? .success(url) : .failure(.notMeow(host: host))
        } catch let e as URLError {
            switch e.code {
            case .notConnectedToInternet, .dataNotAllowed:
                return .failure(.offline)
            case .appTransportSecurityRequiresSecureConnection:
                return .failure(.insecure)
            case .serverCertificateUntrusted, .serverCertificateHasBadDate, .serverCertificateHasUnknownRoot,
                 .serverCertificateNotYetValid, .secureConnectionFailed:
                return .failure(.untrusted(host: host))
            default:
                return .failure(.unreachable(host: host))
            }
        } catch let e as MeowAPIError where e.status >= 500 && !e.code.isEmpty {
            // Meow's own error body: the server is there but not working.
            return .failure(.unreachable(host: host))
        } catch {
            // Another error status, or a body that isn't Meow's health report.
            return .failure(.notMeow(host: host))
        }
    }

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
        guard let api else { return }
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

    /// The command onboarding recommends. The CLI prompts for the Device Key,
    /// so the key never lands in shell history.
    var setupCommand: String { Self.setupCommand(server: server) }

    nonisolated static let cliInstallCommand = "curl -fsSL https://raw.githubusercontent.com/AmazingAng/meow/main/scripts/install.sh | sh"
    nonisolated static let cliHomebrewCommand = "brew install amazingang/tap/meow"

    var installAndSetupCommand: String { Self.installAndSetupCommand(server: server) }

    /// Setup reads its credential from the terminal, never from the copied command.
    nonisolated static func installAndSetupCommand(server: URL?) -> String {
        guard let server else { return cliInstallCommand }
        return "\(cliInstallCommand) -s -- --server \(shellQuoted(commandString(server)))"
    }

    /// Includes the configured server so setup needs no second address entry.
    nonisolated static func setupCommand(server: URL?) -> String {
        guard let server else { return "meow setup" }
        return "meow setup --server \(shellQuoted(commandString(server)))"
    }

    func loginCommand(for key: String) -> String {
        "meow login --key \(Self.shellQuoted(key)) --server \(Self.shellQuoted(Self.commandString(server)))"
    }

    func curlCommand(for key: String) -> String {
        "curl -H \"Authorization: Bearer \(key)\" -d \"Hello from curl\" \(Self.shellQuoted(Self.commandString(server) + "/v1/notifications"))"
    }

    nonisolated private static func commandString(_ server: URL?) -> String {
        var s = server?.absoluteString ?? ""
        while s.hasSuffix("/") { s.removeLast() }
        return s
    }

    /// Single-quotes `s` for a POSIX shell unless every character is safe bare.
    nonisolated static func shellQuoted(_ s: String) -> String {
        let safe = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.:/@%+=,")
        if !s.isEmpty, s.unicodeScalars.allSatisfy(safe.contains) { return s }
        return "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }

    func sendTestNotification(priority: String = "normal") async {
        guard let deviceKey, let server else { return }
        let generation = sessionGeneration
        sendingTestCount += 1
        defer { if generation == sessionGeneration { sendingTestCount -= 1 } }
        do {
            let sent = try await MeowAPI(server: server, token: deviceKey).notify(
                title: String(localized: "Meow!"),
                body: String(localized: "Your agents can reach you now."), priority: priority)
            guard generation == sessionGeneration else { return }
            sentTestIDs.insert(sent.id)
            if let scope = inboxScope {
                MeowShared.defaults.set(["scope": scope, "ids": Array(sentTestIDs)], forKey: Keys.sentTests)
            }
            toast = Toast(message: isNotificationPermissionGranted
                ? String(localized: "Test notification sent.")
                : String(localized: "Sent. Notifications are off, so it's only in your inbox."))
            await refresh()
        } catch {
            guard generation == sessionGeneration else { return }
            alert = Self.describe(error)
        }
    }

    // MARK: Push

    func requestNotificationPermission() async {
        _ = try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge])
        await refreshPermission()
    }

    func refreshPermission() async {
        let settings = await UNUserNotificationCenter.current().notificationSettings()
        permission = settings.authorizationStatus
        timeSensitiveSetting = settings.timeSensitiveSetting
        if isNotificationPermissionGranted {
            UIApplication.shared.registerForRemoteNotifications()
        }
    }

    func didReceiveAPNsToken(_ token: String) {
        apnsToken = token
        if isRegistered { sendAPNsToken(token) }
    }

    private func sendAPNsToken(_ token: String) {
        pushTask = Task { await configurePush() }
    }

    /// Device registration and binding are automatic; existing routes use
    /// their device-scoped management credential for token updates.
    func configurePush() async {
        guard !isConfiguringPush, !isChangingServer, isRegistered,
              let token = apnsToken, let server, let mobileToken = MeowShared.deviceToken else { return }
        let generation = sessionGeneration
        let api = MeowAPI(server: server, token: mobileToken, session: pushSession)
        isConfiguringPush = true
        defer {
            if generation == sessionGeneration {
                isConfiguringPush = false
                if let latest = apnsToken, latest != token { sendAPNsToken(latest) }
            }
        }
        #if DEBUG
        let environment = "sandbox"
        #else
        let environment = "production"
        #endif
        do {
            let config = try await api.pushConfiguration()
            guard generation == sessionGeneration, !Task.isCancelled else { return }
            if config.mode == "direct" {
                let device = try await api.updateDevice(apnsToken: token, apnsEnv: environment)
                guard generation == sessionGeneration, !Task.isCancelled else { return }
                MeowShared.deviceID = device.id
                gatewayURL = nil
                if let old = MeowShared.gatewayBinding {
                    MeowShared.saveGatewayBinding(nil)
                    Self.revokeGateway(old, session: pushSession)
                }
            } else if config.mode == "gateway", let raw = config.gatewayUrl,
                      let gateway = MeowTransport.gatewayURL(raw) {
                gatewayURL = gateway
                if MeowShared.deviceID == nil {
                    let device = try await api.device()
                    guard generation == sessionGeneration, !Task.isCancelled else { return }
                    MeowShared.deviceID = device.id
                }
                guard let deviceID = MeowShared.deviceID else { throw URLError(.userAuthenticationRequired) }
                var binding = MeowShared.gatewayBinding
                if binding?.serverURL != server || binding?.gatewayURL != gateway || binding?.deviceID != deviceID {
                    binding = nil
                }
                if let current = binding {
                    do {
                        try await MeowAPI(server: gateway, token: current.managementToken, session: pushSession)
                            .updateGateway(current.routeID, apnsToken: token, environment: environment)
                    } catch let e as MeowAPIError where e.status == 401 || e.status == 404 || e.status == 410 {
                        guard generation == sessionGeneration else { return }
                        MeowShared.saveGatewayBinding(nil)
                        binding = nil
                    }
                }
                guard generation == sessionGeneration, !Task.isCancelled else { return }
                if binding == nil {
                    let bindingID = UUID().uuidString
                    let registration = try await MeowAPI(server: gateway, token: nil, session: pushSession)
                        .enrollGateway(apnsToken: token, environment: environment, bindingID: bindingID)
                    let fresh = GatewayBinding(gatewayURL: gateway, serverURL: server, deviceID: deviceID,
                                               routeID: registration.id, managementToken: registration.managementToken,
                                               pushToken: registration.pushToken, bindingID: registration.bindingId)
                    guard generation == sessionGeneration, !Task.isCancelled else { Self.revokeGateway(fresh, session: pushSession); return }
                    guard registration.bindingId == bindingID, registration.id.hasPrefix("gwd_"),
                          registration.managementToken.hasPrefix("gwm_"), registration.pushToken.hasPrefix("gwp_") else {
                        Self.revokeGateway(fresh, session: pushSession)
                        throw URLError(.badServerResponse)
                    }
                    let old = MeowShared.gatewayBinding
                    guard MeowShared.saveGatewayBinding(fresh) else {
                        Self.revokeGateway(fresh, session: pushSession)
                        throw URLError(.cannotWriteToFile)
                    }
                    if let old { Self.revokeGateway(old, session: pushSession) }
                    binding = fresh
                }
                guard let binding else { return }
                try await api.bindGateway(binding)
                guard generation == sessionGeneration, !Task.isCancelled else { return }
                    } else { throw URLError(.badServerResponse) }
            hasPushToken = true
            pushRegistrationError = nil
        } catch {
            guard generation == sessionGeneration else { return }
            hasPushToken = false
            pushRegistrationError = Self.describe(error)
        }
    }

    nonisolated private static func revokeGateway(_ binding: GatewayBinding, session: URLSession) {
        Task {
            try? await MeowAPI(server: binding.gatewayURL, token: binding.managementToken, session: session)
                .revokeGateway(binding.routeID)
        }
    }

    // MARK: Inbox

    private var inboxScope: String? {
        guard let server, let token = MeowShared.deviceToken else { return nil }
        return InboxStore.scope(server: server, token: token)
    }

    private var matchingNotifications: [MeowNotification] {
        let query = search.trimmingCharacters(in: .whitespacesAndNewlines)
        return savedNotifications.filter { notification in
            let inFilter: Bool
            switch filter {
            case .all: inFilter = true
            case .unread: inFilter = notification.isUnread
            case .pending: inFilter = notification.question?.isOpen() == true
            }
            return inFilter && (query.isEmpty || notification.title.localizedCaseInsensitiveContains(query)
                || notification.body.localizedCaseInsensitiveContains(query)
                || notification.source.localizedCaseInsensitiveContains(query)
                || notification.context?.searchText.localizedCaseInsensitiveContains(query) == true)
        }
    }

    private func ordered(_ items: [MeowNotification]) -> [MeowNotification] {
        items.sorted { $0.createdAt == $1.createdAt ? $0.id > $1.id : $0.createdAt > $1.createdAt }
    }

    private func hydrateInbox() async {
        guard isRegistered, !isChangingServer, let scope = inboxScope, hydratedScope != scope else { return }
        let generation = sessionGeneration
        if hydrationTask == nil {
            let store = inboxStore
            hydrationTask = Task { try await store.load(scope: scope) }
        }
        guard let task = hydrationTask else { return }
        do {
            let items = try await task.value
            guard generation == sessionGeneration, !isChangingServer, hydratedScope != scope else { return }
            savedNotifications = ordered(items ?? [])
            hydratedScope = scope
            if let items {
                hasLoaded = true
                counts = InboxCounts(unread: items.filter(\.isUnread).count,
                                     pending: items.filter { $0.question?.isOpen() == true }.count)
                updateBadge()
            }
        } catch {
            guard generation == sessionGeneration, !isChangingServer else { return }
            hydrationTask = nil
            inboxStorageError = String(localized: "Couldn't read saved messages. (\(error.localizedDescription))")
        }
    }

    private func persistInbox() {
        guard !isChangingServer, let scope = inboxScope, hydratedScope == scope else { return }
        let generation = sessionGeneration
        persistenceRevision &+= 1
        let revision = persistenceRevision
        inboxStore.enqueueSave(savedNotifications, scope: scope) { [weak self] result in
            Task { @MainActor in
                guard let self, generation == self.sessionGeneration, !self.isChangingServer,
                      revision == self.persistenceRevision else { return }
                switch result {
                case .success: self.inboxStorageError = nil
                case .failure(let error):
                    self.inboxStorageError = String(localized: "Couldn't save messages on this iPhone. (\(error.localizedDescription))")
                }
            }
        }
    }

    private func changed(_ ids: [String]) {
        mutationRevision &+= 1
        for id in ids { itemRevisions[id] = mutationRevision }
        persistInbox()
    }

    func notification(id: String) -> MeowNotification? {
        savedNotifications.first { $0.id == id } ?? details[id]
    }

    func refresh() async {
        guard isRegistered, !isChangingServer, let api, let scope = inboxScope else { return }
        loadGeneration += 1
        let generation = loadGeneration
        isLoading = true
        defer { if generation == loadGeneration { isLoading = false } }
        await hydrateInbox()
        guard generation == loadGeneration, !isChangingServer else { return }
        let revision = mutationRevision
        let countRevision = countsRevision
        do {
            async let remoteCounts = try? await api.counts()
            let items = try await api.allInbox()
            guard generation == loadGeneration, !isChangingServer else { return }
            let current = Dictionary(savedNotifications.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
            var merged = current
            for var item in items {
                // Server omission/expiry is not a command to erase local history.
                if item.readAt == nil { item.readAt = current[item.id]?.readAt }
                if item.question?.status == .pending, let previous = current[item.id]?.question,
                   previous.status != .pending { item.question = previous }
                merged[item.id] = item
            }
            serverNotificationIDs = Set(items.map(\.id))
            serverUnreadIDs = Set(items.filter(\.isUnread).map(\.id))
            serverPendingIDs = Set(items.filter { $0.question?.isOpen() == true }.map(\.id))
            for (id, changedAt) in itemRevisions where changedAt > revision {
                // nil also protects a server-confirmed deletion from an older snapshot.
                merged[id] = current[id]
            }
            savedNotifications = ordered(Array(merged.values))
            let present = Set(savedNotifications.map(\.id))
            details = details.filter { present.contains($0.key) }
            hasLoaded = true
            loadError = nil
            // A failed local read must never turn a short server cache into a
            // complete local snapshot. Keep the disk history intact for retry.
            if hydratedScope == scope { persistInbox() }
            let c = await remoteCounts
            if generation == loadGeneration, countRevision == countsRevision {
                serverCounts = c
                if let c, mutationRevision == revision { counts = countsIncludingHistory(c) }
                else {
                    counts = InboxCounts(unread: savedNotifications.filter(\.isUnread).count,
                        pending: savedNotifications.filter { $0.question?.isOpen() == true }.count)
                }
                countsRevision += 1
            }
            guard generation == loadGeneration, !isChangingServer else { return }
            updateBadge()
        } catch {
            guard generation == loadGeneration, !Self.isCancellation(error) else { return }
            loadError = Self.describe(error)
            hasLoaded = true
        }
    }

    // MARK: Local grouping

    private func rebuildInboxGroups() {
        groupingRevision += 1
        let revision = groupingRevision
        groupingTask?.cancel()
        guard inboxMode != .messages, !isChangingServer, let scope = inboxScope else {
            allInboxGroups = []; isGroupingInbox = false
            return
        }
        let items = savedNotifications, mode = inboxMode, query = search
        let unreadOnly = filter == .unread, rules = groupingStore.load(scope: scope)
        isGroupingInbox = true
        groupingTask = Task {
            let work = Task.detached(priority: .userInitiated) {
                InboxGrouping.build(items, mode: mode, scope: scope, rules: rules, query: query, unreadOnly: unreadOnly)
            }
            let groups = await withTaskCancellationHandler(operation: { await work.value }, onCancel: { work.cancel() })
            guard !Task.isCancelled, revision == groupingRevision, scope == inboxScope, !isChangingServer else { return }
            allInboxGroups = groups
            isGroupingInbox = false
        }
    }

    func waitForInboxGrouping() async { await groupingTask?.value }
    func loadMoreInboxGroups() { groupDisplayLimit += 50 }
    func inboxGroup(_ id: String) -> InboxGroup? { allInboxGroups.first { $0.id == id } }

    func mergeInboxGroups(_ ids: Set<String>) {
        guard !isGroupingInbox, !isChangingServer, let scope = inboxScope, inboxMode != .messages else { return }
        let selected = allInboxGroups.filter { ids.contains($0.id) }
        guard selected.count >= 2 else { return }
        var members: [String: InboxGroupMember] = [:]
        for group in selected { for member in group.members { members[member.id] = member } }
        let memberIDs = Set(members.keys)
        var rules = groupingStore.load(scope: scope)
        rules.removeAll { $0.mode == inboxMode && !$0.members.allSatisfy { !memberIDs.contains($0.id) } }
        rules.append(InboxMergeRule(mode: inboxMode, members: members.values.sorted { $0.id < $1.id }))
        groupingStore.save(rules, scope: scope)
        rebuildInboxGroups()
    }

    func unmergeInboxGroup(_ group: InboxGroup) {
        guard !isChangingServer, let scope = inboxScope, let mergeID = group.mergeID else { return }
        var rules = groupingStore.load(scope: scope)
        rules.removeAll { $0.id == mergeID && $0.mode == group.mode }
        groupingStore.save(rules, scope: scope)
        rebuildInboxGroups()
    }

    /// Freeze the complete matching ID set, including rows not yet displayed.
    func markMatchingRead(_ ids: Set<String>) async {
        guard !isMarkingGroupRead, !isMarkingAllRead, !isChangingServer else { return }
        let generation = sessionGeneration
        let targets = ids.filter { notification(id: $0)?.isUnread == true && readRequests[$0] == nil }.sorted()
        guard !targets.isEmpty else { return }
        isMarkingGroupRead = true
        failedGroupReadIDs = []
        defer { if generation == sessionGeneration { isMarkingGroupRead = false } }
        var failed: Set<String> = []
        await withTaskGroup(of: (String, Bool).self) { group in
            var next = targets.makeIterator()
            for _ in 0..<4 {
                if let id = next.next() { group.addTask { (id, await self.markRead(id, reportFailure: false)) } }
            }
            while let (id, success) = await group.next() {
                guard generation == sessionGeneration, !isChangingServer else { group.cancelAll(); break }
                if !success { failed.insert(id) }
                if let id = next.next() { group.addTask { (id, await self.markRead(id, reportFailure: false)) } }
            }
        }
        guard generation == sessionGeneration, !isChangingServer else { return }
        failedGroupReadIDs = failed
        if !failed.isEmpty {
            toast = Toast(message: String(localized: "Couldn't mark \(failed.count) messages as read. Retry from the group menu."))
        }
    }

    func loadMore() async {
        guard nextCursor != nil else { return }
        displayLimit += 50
    }

    /// Counts alone cannot distinguish cache expiry from an unread local message.
    func refreshCounts() async {
        if isLoading {
            // An existing full sync will update server membership. Do not start a
            // second snapshot that could overwrite a just-accepted local mutation.
            counts = InboxCounts(unread: savedNotifications.filter(\.isUnread).count,
                                 pending: savedNotifications.filter { $0.question?.isOpen() == true }.count)
            countsRevision += 1
            updateBadge()
            return
        }
        await refresh()
    }

    private func countsIncludingHistory(_ remote: InboxCounts) -> InboxCounts {
        var result = remote
        for item in savedNotifications {
            if !serverNotificationIDs.contains(item.id) {
                if item.isUnread { result.unread += 1 }
                if item.question?.isOpen() == true { result.pending += 1 }
            } else {
                if serverUnreadIDs.contains(item.id), !item.isUnread { result.unread -= 1 }
                if serverPendingIDs.contains(item.id), item.question?.isOpen() != true { result.pending -= 1 }
            }
        }
        result.unread = max(0, result.unread)
        result.pending = max(0, result.pending)
        return result
    }

    /// Fetches one notification; returns whether it is available to show.
    func loadDetail(_ id: String) async -> Bool {
        guard let api else { return false }
        let generation = sessionGeneration
        await hydrateInbox()
        guard generation == sessionGeneration, !isChangingServer else { return false }
        let revision = deletionRevision
        do {
            var notification = try await api.notification(id)
            guard generation == sessionGeneration else { return false }
            guard revision == deletionRevision else { return self.notification(id: id) != nil }
            if notification.readAt == nil { notification.readAt = self.notification(id: id)?.readAt }
            if notification.question?.status == .pending, let previous = self.notification(id: id)?.question,
               previous.status != .pending { notification.question = previous }
            upsert(notification)
            return true
        } catch let error as MeowAPIError where error.status == 404 {
            guard generation == sessionGeneration, !isChangingServer else { return false }
            // The server only retains a recent delivery cache. Keep saved history.
            return notification(id: id) != nil
        } catch {
            guard generation == sessionGeneration, !isChangingServer else { return false }
            return notification(id: id) != nil
        }
    }

    func deleteNotification(_ id: String) async {
        await deleteMessages(id: id, interval: nil)
    }

    func deleteNotifications(in interval: InboxDeletionInterval) async {
        await deleteMessages(id: nil, interval: interval)
    }

    private func deleteMessages(id: String?, interval: InboxDeletionInterval?) async {
        guard isRegistered, !isChangingServer, !isDeletingNotifications, let api else { return }
        let generation = sessionGeneration
        isDeletingNotifications = true
        defer { if generation == sessionGeneration { isDeletingNotifications = false } }
        await hydrateInbox()
        guard generation == sessionGeneration, !isChangingServer else { return }
        do {
            if let id {
                do { try await api.deleteNotification(id) }
                catch let error as MeowAPIError where error.status == 404 && error.code == "not_found" {
                    // Already absent from this server; explicit user deletion still
                    // removes the local copy. Unsupported endpoints have no such code.
                }
            } else if let interval { try await api.deleteNotifications(in: interval) }
            guard generation == sessionGeneration, !isChangingServer else { return }
            // Invalidate snapshots started before the server confirmed deletion,
            // including details not previously present in the saved inbox.
            deletionRevision += 1
            loadGeneration += 1
            isLoading = false
            let matches: (MeowNotification) -> Bool = { n in
                if let id { return n.id == id }
                return interval?.contains(n.createdAt) == true
            }
            let removed = Set((savedNotifications + Array(details.values)).filter(matches).map(\.id))
            savedNotifications.removeAll(where: matches)
            details = details.filter { !removed.contains($0.key) }
            for id in removed { readRequests[id] = nil }
            serverNotificationIDs.subtract(removed)
            serverUnreadIDs.subtract(removed)
            serverPendingIDs.subtract(removed)
            serverCounts = nil
            inboxPath.removeAll { removed.contains($0) }
            changed(Array(removed))
            counts = InboxCounts(unread: savedNotifications.filter(\.isUnread).count,
                                 pending: savedNotifications.filter { $0.question?.isOpen() == true }.count)
            countsRevision += 1
            updateBadge()
        } catch {
            guard generation == sessionGeneration, !isChangingServer, !Self.isCancellation(error) else { return }
            alert = String(localized: "Couldn't delete messages. \(Self.describe(error))")
        }
    }

    @discardableResult
    func markRead(_ id: String, reportFailure: Bool = true) async -> Bool {
        guard !Task.isCancelled, !isChangingServer, readRequests[id] == nil, var n = notification(id: id), n.isUnread, let api else { return false }
        let generation = sessionGeneration
        let revision = countsRevision
        let request = UUID()
        let readAt = Date.now
        let unreadDelta = min(1, counts.unread)
        readRequests[id] = request
        n.readAt = readAt
        upsert(n)
        counts.unread -= unreadDelta
        updateBadge()
        defer { if readRequests[id] == request { readRequests[id] = nil } }
        do {
            try await api.markRead(id)
        } catch let error as MeowAPIError where error.status == 404 && error.code == "not_found" {
            // The saved message outlived its server cache. Its read state is local.
        } catch {
            guard generation == sessionGeneration, readRequests[id] == request else { return false }
            if var current = notification(id: id), current.readAt == readAt {
                current.readAt = nil
                upsert(current)
                if revision == countsRevision { counts.unread += unreadDelta }
                else if let serverCounts { counts = countsIncludingHistory(serverCounts) }
                updateBadge()
            }
            if reportFailure && !Self.isCancellation(error) {
                toast = Toast(message: String(localized: "Couldn't mark as read. \(Self.describe(error))"))
            }
            return false
        }
        return generation == sessionGeneration && !isChangingServer
    }

    func markAllRead() async {
        guard !isChangingServer, !isMarkingAllRead, !isMarkingGroupRead, let api else { return }
        let generation = sessionGeneration
        isMarkingAllRead = true
        defer { if generation == sessionGeneration { isMarkingAllRead = false } }
        do {
            try await api.markAllRead()
            guard generation == sessionGeneration, !isChangingServer else { return }
            readRequests = [:]
            var updated = savedNotifications
            for i in updated.indices where updated[i].readAt == nil { updated[i].readAt = .now }
            savedNotifications = updated
            for (id, var n) in details where n.readAt == nil {
                n.readAt = .now
                details[id] = n
            }
            counts.unread = 0
            countsRevision += 1
            changed(savedNotifications.map(\.id))
            updateBadge()
        } catch {
            guard generation == sessionGeneration, !isChangingServer, !Self.isCancellation(error) else { return }
            alert = Self.describe(error)
        }
    }

    /// Opens a notification, e.g. after the user taps a push.
    func open(_ id: String) {
        selectedTab = .inbox
        inboxPath = [id]
    }

    private func upsert(_ n: MeowNotification) {
        if let i = savedNotifications.firstIndex(where: { $0.id == n.id }) {
            savedNotifications[i] = n
        } else {
            savedNotifications.append(n)
            savedNotifications = ordered(savedNotifications)
        }
        details[n.id] = n
        changed([n.id])
    }

    private func update(_ q: MeowQuestion) {
        for i in savedNotifications.indices where savedNotifications[i].question?.id == q.id {
            readRequests[savedNotifications[i].id] = nil
            savedNotifications[i].question?.apply(q)
            if savedNotifications[i].readAt == nil { savedNotifications[i].readAt = q.resolvedAt ?? .now }
        }
        if var n = details[q.notificationId] {
            readRequests[n.id] = nil
            n.question?.apply(q)
            if n.readAt == nil { n.readAt = q.resolvedAt ?? .now }
            details[n.id] = n
        }
        changed([q.notificationId])
    }

    private func updateBadge() {
        let unread = counts.unread
        Task { try? await UNUserNotificationCenter.current().setBadgeCount(unread) }
    }

    // MARK: Answers

    func answer(_ questionID: String, with answer: String, identity expectedIdentity: NotificationIdentity? = nil) async -> AnswerResult {
        await hydrateInbox()
        guard isRegistered, !isChangingServer else {
            return .refused(String(localized: "The server is changing. Please try again afterward."))
        }
        guard let identity = NotificationIdentity.current,
              expectedIdentity == nil || expectedIdentity == identity else {
            return .refused(String(localized: "The server has changed."))
        }
        let generation = sessionGeneration
        let outcome = await replies.submit(questionID: questionID, answer: answer, identity: identity)
        let failures = await replies.failed
        guard generation == sessionGeneration, !isChangingServer else {
            return .refused(String(localized: "The server has changed."))
        }
        let result = apply(outcome, to: questionID)
        failedReplies = failures
        await refreshCounts()
        return result
    }

    /// Answers from a notification action. The user isn't looking at the app,
    /// so a refusal is reported with a local notification.
    func answerFromNotification(_ questionID: String, with answer: String, identity: NotificationIdentity) async {
        guard NotificationIdentity.current == identity else { return }
        guard case .refused(let reason) = await self.answer(questionID, with: answer, identity: identity),
              NotificationIdentity.current == identity else { return }
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
        await hydrateInbox()
        guard generation == sessionGeneration, !isChangingServer else { return }
        let results = await replies.flush()
        let pending = await replies.pending
        let failures = await replies.failed
        guard generation == sessionGeneration, !isChangingServer else { return }
        var reasons: [String] = []
        for (reply, outcome) in results {
            let result = apply(outcome, to: reply.questionID)
            if case .discarded = outcome { continue }
            if case .refused(let reason) = result { reasons.append(reason) }
        }
        queuedReplies = Dictionary(pending.map { ($0.questionID, $0) }, uniquingKeysWith: { first, _ in first })
        failedReplies = failures
        if !reasons.isEmpty {
            alert = reasons.joined(separator: "\n")
        }
    }

    func dismissFailedReply(_ reply: PendingReply) async {
        let generation = sessionGeneration
        do {
            try await replies.dismissFailure(reply.idempotencyKey)
            let failures = await replies.failed
            guard generation == sessionGeneration else { return }
            failedReplies = failures
        } catch {
            guard generation == sessionGeneration else { return }
            alert = Self.describe(error)
        }
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
            return .queued
        case .alreadyQueued(let reply):
            queuedReplies[questionID] = reply
            return .refused(String(localized: "Already sending “\(reply.answer)”."))
        }
    }

    /// Retain earlier versions' queued answers without sending them in V1.
    func loadSavedReplyRecords() async {
        let generation = sessionGeneration
        let pending = await replies.pending
        let failures = await replies.failed
        guard generation == sessionGeneration, !isChangingServer else { return }
        queuedReplies = Dictionary(pending.map { ($0.questionID, $0) }, uniquingKeysWith: { first, _ in first })
        failedReplies = failures
    }

    // MARK: Errors

    private static func isCancellation(_ error: Error) -> Bool {
        error is CancellationError || (error as? URLError)?.code == .cancelled
    }

    static func describe(_ error: Error) -> String {
        if let e = error as? MeowAPIError {
            if e.status == 401 {
                return String(localized: "This iPhone is no longer registered with the server. In Settings, choose Re-register this server.")
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
