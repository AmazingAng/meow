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
    private(set) var isRotatingKey = false
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
    private var messageProjection = InboxProjection()
    private(set) var isProjectingMessages = false
    private var projectionMatchesQuery: Bool {
        messageProjection.query == search.trimmingCharacters(in: .whitespacesAndNewlines)
            && messageProjection.unreadOnly == (filter == .unread)
            && messageProjection.pendingOnly == (filter == .pending)
    }
    var notifications: [MeowNotification] {
        guard projectionMatchesQuery else { return [] }
        if filter == .pending { return messageProjection.pending }
        if filter == .all, messageProjection.query.isEmpty {
            return messageProjection.pending + Array(messageProjection.activity.prefix(displayLimit))
        }
        return Array(messageProjection.matches.prefix(displayLimit))
    }
    private(set) var counts = InboxCounts()
    var nextCursor: String? {
        guard projectionMatchesQuery else { return nil }
        let count = filter == .all && messageProjection.query.isEmpty
            ? messageProjection.activity.count : messageProjection.matches.count
        return filter != .pending && count > displayLimit ? "local-\(displayLimit)" : nil
    }
    private(set) var isLoading = false
    private(set) var isDeletingNotifications = false
    private(set) var hasLoaded = false
    private(set) var hasSyncedInbox = false
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
    @ObservationIgnored private let saveDeviceKey: (String) -> Bool
    @ObservationIgnored private var refreshTask: Task<Void, Never>?
    @ObservationIgnored private var refreshTaskID: UUID?
    @ObservationIgnored private var notificationFetches: [String: (UUID, Task<Void, Never>)] = [:]
    private struct PendingInboxChange: Sendable {
        var item: MeowNotification?
        var revision: UInt64
    }
    @ObservationIgnored private var pendingInboxChanges: [String: PendingInboxChange] = [:]
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
    @ObservationIgnored private var deletionRevision = 0

    private enum Keys {
        static let onboarded = "onboarded"
        static let deviceKey = "device_key"
        static let deviceKeyID = "device_key_id"
        static let sentTests = "onboarding_sent_tests"
        static let pendingKeyRevocation = "key_pending_revocation"
    }

    init(replies: ReplyQueue = .shared, pushSession: URLSession = MeowTransport.gateway,
         inboxStore: InboxStore = .shared, groupingStore: InboxGroupingStore = InboxGroupingStore(),
         saveDeviceKey: @escaping (String) -> Bool = { Keychain.set($0, for: "device_key") }) {
        self.replies = replies
        self.pushSession = pushSession
        self.inboxStore = inboxStore
        self.saveDeviceKey = saveDeviceKey
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
            await hydrateInbox()
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
        refreshTask?.cancel(); refreshTask = nil; refreshTaskID = nil
        for entry in notificationFetches.values { entry.1.cancel() }
        notificationFetches = [:]
        pendingInboxChanges = [:]
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
        isProjectingMessages = false
        messageProjection = InboxProjection()
        isMarkingGroupRead = false
        isMarkingAllRead = false
        failedGroupReadIDs = []
        savedNotifications = []
        details = [:]
        counts = InboxCounts()
        displayLimit = 50
        groupDisplayLimit = 50
        filter = .all
        search = ""
        mutationRevision = 0
        itemRevisions = [:]
        hasLoaded = false
        hasSyncedInbox = false
        loadError = nil
        inboxStorageError = nil
        inboxPath = []
        selectedTab = .inbox
        server = newServer
        MeowShared.server = newServer
        queuedReplies = [:]
        failedReplies = []
        readRequests = [:]
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
        guard !isRotatingKey, !isRegistering, !isChangingServer, let api, let token = api.token else { return }
        let generation = sessionGeneration
        let scope = InboxStore.scope(server: api.server, token: token)
        isRotatingKey = true
        defer { isRotatingKey = false }
        alert = nil
        toast = nil
        do {
            let pending = MeowShared.defaults.dictionary(forKey: Keys.pendingKeyRevocation)
            var old = pending?["scope"] as? String == scope ? pending?["id"] as? String : nil
            if old == nil {
                old = MeowShared.defaults.string(forKey: Keys.deviceKeyID)
                guard deviceKey == nil || old != nil else {
                    alert = String(localized: "The current Device Key's ID is unavailable. Your key has been kept.")
                    return
                }
                let key = try await api.createKey(name: "Default", scopes: ["notify", "ask"])
                guard keyRotationIsCurrent(generation, api: api) else { return }
                guard let secret = key.secret, secret.hasPrefix("meow_"), !key.id.isEmpty else {
                    alert = String(localized: "The server did not return a usable Device Key. Your current key has been kept.")
                    return
                }
                guard saveDeviceKey(secret) else {
                    // The unsaved replacement was never shared. Cleanup is best-effort;
                    // never revoke the user's working key after a storage failure.
                    try? await api.revokeKey(key.id)
                    guard keyRotationIsCurrent(generation, api: api) else { return }
                    alert = String(localized: "Couldn't save the new Device Key to the Keychain. Your current key has been kept.")
                    return
                }
                if let old { MeowShared.defaults.set(["scope": scope, "id": old], forKey: Keys.pendingKeyRevocation) }
                MeowShared.defaults.set(key.id, forKey: Keys.deviceKeyID)
                deviceKey = secret
            }
            if let old {
                do {
                    try await api.revokeKey(old)
                } catch let error as MeowAPIError where error.status == 404 { /* already absent */ }
            }
            guard keyRotationIsCurrent(generation, api: api) else { return }
            MeowShared.defaults.removeObject(forKey: Keys.pendingKeyRevocation)
            // During onboarding the computer isn't connected yet, so there is nothing to update.
            toast = Toast(message: hasFinishedOnboarding
                ? String(localized: "New Device Key created. Update it in your CLI.")
                : String(localized: "New Device Key created."))
        } catch {
            guard keyRotationIsCurrent(generation, api: api) else { return }
            let pending = MeowShared.defaults.dictionary(forKey: Keys.pendingKeyRevocation)
            alert = pending?["scope"] as? String == scope
                ? String(localized: "The new key is saved, but the old key could not be revoked. Tap Reset Device Key again to retry. (\(Self.describe(error)))")
                : Self.describe(error)
        }
    }

    private func keyRotationIsCurrent(_ generation: Int, api: MeowAPI) -> Bool {
        generation == sessionGeneration && server == api.server && MeowShared.deviceToken == api.token
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

    /// Shows only the fixed prefix and the last 4 characters, so the preview
    /// reveals as little of the secret as possible.
    nonisolated static func maskedDeviceKey(_ key: String) -> String {
        key.hasPrefix("meow_") ? "meow_…\(key.suffix(4))" : "…\(key.suffix(4))"
    }

    /// Prompts on stderr and hides typing, so the key stays off the screen and
    /// out of shell history. stdout is left for curl's response.
    nonisolated static let scriptKeyInputCommand = "printf 'Device Key: ' >&2; read -rs MEOW_KEY; echo >&2"

    /// The copied example references a shell variable, never the saved key.
    var scriptNotificationCommand: String { Self.scriptNotificationCommand(server: server) }

    nonisolated static func scriptNotificationCommand(server: URL?) -> String {
        let endpoint = shellQuoted(commandString(server) + "/v1/notifications")
        return "curl -fsS -H \"Authorization: Bearer $MEOW_KEY\" -d \"Hello from curl\" \(endpoint)"
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

    private func ordered(_ items: [MeowNotification]) -> [MeowNotification] {
        items.sorted { $0.createdAt == $1.createdAt ? $0.id > $1.id : $0.createdAt > $1.createdAt }
    }

    private func hydrateInbox() async {
        guard isRegistered, !isChangingServer, let scope = inboxScope, hydratedScope != scope else { return }
        let generation = sessionGeneration
        if hydrationTask == nil {
            let store = inboxStore
            hydrationTask = Task { [weak self] in
                guard let self, generation == self.sessionGeneration, !self.isChangingServer else { throw CancellationError() }
                try await store.prepareForApp()
                guard generation == self.sessionGeneration, !self.isChangingServer else { throw CancellationError() }
                try await store.activate(scope: scope)
                return try await store.load(scope: scope)
            }
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

    private func persistInbox(_ ids: Set<String> = []) {
        guard !isChangingServer, let scope = inboxScope, hydratedScope == scope else { return }
        let generation = sessionGeneration
        persistenceRevision &+= 1
        let revision = persistenceRevision
        var remaining = ids
        for item in savedNotifications where ids.contains(item.id) {
            pendingInboxChanges[item.id] = PendingInboxChange(item: item, revision: revision)
            remaining.remove(item.id)
        }
        for id in remaining { pendingInboxChanges[id] = PendingInboxChange(item: details[id], revision: revision) }
        let batch = pendingInboxChanges
        let upserts = batch.values.compactMap(\.item)
        let deleted = Set(batch.filter { $0.value.item == nil }.keys)
        inboxStore.enqueueChanges(upserts, deleting: deleted, scope: scope) { [weak self] result in
            Task { @MainActor in
                guard let self, generation == self.sessionGeneration, !self.isChangingServer else { return }
                if case .success = result {
                    for (id, change) in batch where self.pendingInboxChanges[id]?.revision == change.revision {
                        self.pendingInboxChanges[id] = nil
                    }
                }
                guard revision == self.persistenceRevision else { return }
                switch result {
                case .success: self.inboxStorageError = nil
                case .failure(let error):
                    self.inboxStorageError = String(
                        localized: "Couldn't save messages on this iPhone. (\(error.localizedDescription))")
                }
            }
        }
    }

    private func changed(_ ids: [String], persist: Bool = true) {
        mutationRevision &+= 1
        for id in ids { itemRevisions[id] = mutationRevision }
        if persist { persistInbox(Set(ids)) }
    }

    func notification(id: String) -> MeowNotification? {
        savedNotifications.first { $0.id == id } ?? details[id]
    }

    /// A verified foreground wake already identifies the one changed record.
    /// Do not redownload the entire server cache for every incoming push.
    func receiveNotification(_ id: String, identity: NotificationIdentity) async {
        guard isRegistered, !isChangingServer, identity == NotificationIdentity.current else { return }
        if let existing = notificationFetches[id] { await existing.1.value; return }
        let generation = sessionGeneration, request = UUID()
        let task = Task { [weak self] in
            guard let self else { return }
            let loaded = await loadDetail(id)
            guard loaded, generation == sessionGeneration, identity == NotificationIdentity.current, !isChangingServer
            else { return }
                counts = InboxCounts(
                unread: savedNotifications.filter(\.isUnread).count,
                pending: savedNotifications.filter { $0.question?.isOpen() == true }.count)
            updateBadge()
        }
        notificationFetches[id] = (request, task)
        await task.value
        if notificationFetches[id]?.0 == request { notificationFetches[id] = nil }
        await waitForInboxGrouping()
    }

    func refresh() async {
        guard isRegistered, !isChangingServer else { return }
        if let task = refreshTask {
            await withTaskCancellationHandler(operation: { await task.value }, onCancel: { task.cancel() })
            await waitForInboxGrouping()
            return
        }
        let id = UUID()
        let task = Task { [weak self] in
            guard let self else { return }
            await refreshSnapshot()
        }
        refreshTaskID = id
        refreshTask = task
        await withTaskCancellationHandler(operation: { await task.value }, onCancel: { task.cancel() })
        if refreshTaskID == id { refreshTask = nil; refreshTaskID = nil }
        await waitForInboxGrouping()
    }

    private func refreshSnapshot() async {
        guard isRegistered, !isChangingServer, let api, let scope = inboxScope else { return }
        loadGeneration += 1
        let generation = loadGeneration
        isLoading = true
        defer { if generation == loadGeneration { isLoading = false } }
        await hydrateInbox()
        await reloadSharedInbox()
        guard generation == loadGeneration, !isChangingServer else { return }
        let revision = mutationRevision
        do {
            let items = try await api.allInbox()
            guard generation == loadGeneration, !isChangingServer else { return }
            let current = Dictionary(savedNotifications.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
            var merged = current
            for var item in items {
                // Server omission/expiry is not a command to erase local history.
                item.readAt = current[item.id]?.readAt
                if item.question?.status == .pending, let previous = current[item.id]?.question,
                   previous.status != .pending { item.question = previous }
                merged[item.id] = item
            }
            for (id, changedAt) in itemRevisions where changedAt > revision { merged[id] = current[id] }
            let changedIDs = Set(merged.compactMap { current[$0.key] == $0.value ? nil : $0.key })
            savedNotifications = ordered(Array(merged.values))
            let present = Set(savedNotifications.map(\.id))
            details = details.filter { present.contains($0.key) }
            hasLoaded = true
            loadError = nil
            hasSyncedInbox = true
            // A failed local read must never turn a short server cache into a
            // complete local snapshot. Keep the disk history intact for retry.
            if hydratedScope == scope { persistInbox(changedIDs) }
            await reloadSharedInbox()
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
        guard !isChangingServer, let scope = inboxScope else {
            allInboxGroups = []; isGroupingInbox = false
            messageProjection = InboxProjection(); isProjectingMessages = false
            return
        }
        let items = savedNotifications, mode = inboxMode, query = search
        let unreadOnly = filter == .unread, pendingOnly = filter == .pending
        let rules = groupingStore.load(scope: scope), now = Date.now
        isGroupingInbox = mode != .messages
        isProjectingMessages = mode == .messages
        groupingTask = Task {
            if mode == .messages {
                let work = Task.detached(priority: .userInitiated) {
                    InboxProjection.build(
                        items, query: query, unreadOnly: unreadOnly, pendingOnly: pendingOnly, now: now)
                }
                let result = await withTaskCancellationHandler(
                    operation: { await work.value }, onCancel: { work.cancel() })
                guard !Task.isCancelled, revision == groupingRevision, scope == inboxScope, !isChangingServer else {
                    return
                }
                messageProjection = result
                isProjectingMessages = false
            } else {
                let work = Task.detached(priority: .userInitiated) {
                    InboxGrouping.project(
                        items, mode: mode, scope: scope, rules: rules, query: query, unreadOnly: unreadOnly)
                }
                let result = await withTaskCancellationHandler(
                    operation: { await work.value }, onCancel: { work.cancel() })
                guard !Task.isCancelled, revision == groupingRevision, scope == inboxScope, !isChangingServer else {
                    return
                }
                if result.rules != rules { groupingStore.save(result.rules, scope: scope) }
                allInboxGroups = result.groups
                isGroupingInbox = false
            }
        }
    }

    func waitForInboxGrouping() async {
        while let task = groupingTask {
            let revision = groupingRevision
            await task.value
            if revision == groupingRevision { return }
        }
    }

    func refreshInboxPresentation() async {
        rebuildInboxGroups()
        await waitForInboxGrouping()
    }

    func loadMoreInboxGroups() { groupDisplayLimit += 50 }
    func inboxGroup(_ id: String) -> InboxGroup? { allInboxGroups.first { $0.id == id } }

    func inboxSelection(_ items: [MeowNotification]) -> InboxMessageSelection? {
        guard !isChangingServer, let scope = inboxScope else { return nil }
        return InboxMessageSelection(scope: scope, ids: Set(items.map(\.id)))
    }

    func markInboxRead(_ selection: InboxMessageSelection) async {
        guard !selection.ids.isEmpty else { return }
        // A second group visit keeps its snapshot while the previous batch finishes.
        while isMarkingGroupRead || isMarkingAllRead || isDeletingNotifications {
            guard selection.scope == inboxScope, !isChangingServer, !Task.isCancelled else { return }
            do { try await Task.sleep(for: .milliseconds(100)) } catch { return }
        }
        guard selection.scope == inboxScope, !isChangingServer, !Task.isCancelled else { return }
        await markMatchingRead(selection.ids)
    }

    func deleteNotifications(_ selection: InboxMessageSelection) async {
        guard selection.scope == inboxScope, isRegistered, !isChangingServer,
              !isDeletingNotifications, !isMarkingGroupRead, !isMarkingAllRead, let api else { return }
        let generation = sessionGeneration
        let available = Set(savedNotifications.map(\.id)).union(details.keys)
        let targets = selection.ids.intersection(available).sorted()
        guard !targets.isEmpty else { return }
        isDeletingNotifications = true
        defer { if generation == sessionGeneration { isDeletingNotifications = false } }
        var removed: Set<String> = [], failed: Set<String> = []
        await withTaskGroup(of: (String, Bool).self) { group in
            var next = targets.makeIterator()
            for _ in 0..<4 {
                if let id = next.next() { group.addTask { (id, await Self.deleteRemoteMessage(id, api: api)) } }
            }
            while let (id, success) = await group.next() {
                guard generation == sessionGeneration, !isChangingServer else { group.cancelAll(); break }
                if success { removed.insert(id) } else { failed.insert(id) }
                if let id = next.next() { group.addTask { (id, await Self.deleteRemoteMessage(id, api: api)) } }
            }
        }
        guard generation == sessionGeneration, !isChangingServer else { return }
        if !removed.isEmpty { removeDeletedMessages(removed); await waitForInboxGrouping() }
        if !failed.isEmpty { alert = String(localized: "Couldn't delete \(failed.count) messages. Try again.") }
    }

    private static func deleteRemoteMessage(_ id: String, api: MeowAPI) async -> Bool {
        await performInboxMutation { try await api.deleteNotification(id) }
    }

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

    /// Read state belongs to this iPhone. Freeze IDs; commit once without HTTP.
    func markMatchingRead(_ ids: Set<String>) async {
        guard !isMarkingGroupRead, !isMarkingAllRead, !isChangingServer else { return }
        isMarkingGroupRead = true
        let generation = sessionGeneration
        defer { if generation == sessionGeneration { isMarkingGroupRead = false } }
        _ = await readLocally(ids, reportFailure: true)
    }

    private func readLocally(_ ids: Set<String>, reportFailure: Bool) async -> Bool {
        guard !Task.isCancelled, isRegistered, !isChangingServer, let scope = inboxScope else { return false }
        await hydrateInbox()
        guard hydratedScope == scope, !isChangingServer else { return false }
        let targets = ids.intersection(Set((savedNotifications + Array(details.values)).filter(\.isUnread).map(\.id)))
            .filter { readRequests[$0] == nil }
        guard !targets.isEmpty else { return false }
        let generation = sessionGeneration, request = UUID(), readAt = Date.now
        for id in targets { readRequests[id] = request }
        defer { for id in targets where readRequests[id] == request { readRequests[id] = nil } }
        do {
            let dirty = pendingInboxChanges.filter { targets.contains($0.key) && $0.value.item != nil }
            try await inboxStore.markLocalRead(targets, scope: scope, at: readAt, pendingItems: dirty.values.compactMap(\.item))
            guard generation == sessionGeneration, !isChangingServer else { return false }
            for (id, change) in dirty where pendingInboxChanges[id]?.revision == change.revision { pendingInboxChanges[id] = nil }
            if pendingInboxChanges.isEmpty { inboxStorageError = nil }
            setBatchRead(targets, at: readAt)
            failedGroupReadIDs = []
            counts = InboxCounts(unread: savedNotifications.filter(\.isUnread).count,
                                 pending: savedNotifications.filter { $0.question?.isOpen() == true }.count)
            let revision = mutationRevision
            if let unread = try? await inboxStore.unreadCount(scope: scope),
               generation == sessionGeneration, revision == mutationRevision, !isChangingServer {
                counts.unread = unread
            }
            guard generation == sessionGeneration, !isChangingServer else { return false }
            updateBadge()
            await waitForInboxGrouping()
            return true
        } catch {
            guard generation == sessionGeneration, !isChangingServer else { return false }
            failedGroupReadIDs = targets
            inboxStorageError = String(localized: "Couldn't save messages on this iPhone. (\(error.localizedDescription))")
            if reportFailure { toast = Toast(message: String(localized: "Couldn't mark \(targets.count) messages as read. Try again.")) }
            return false
        }
    }

    /// Remote deletions are idempotent. Honor the server's rate limit without
    /// treating a missing cached record as an unsupported endpoint.
    private static func performInboxMutation(_ send: @Sendable () async throws -> Void) async -> Bool {
        for attempt in 0..<3 {
            guard !Task.isCancelled else { return false }
            do { try await send(); return true }
            catch let error as MeowAPIError where error.status == 404 && error.code == "not_found" { return true }
            catch let error as MeowAPIError where error.status == 429 && attempt < 2 {
                do { try await Task.sleep(for: error.retryAfter ?? .seconds(1)) } catch { return false }
            }
            catch { return false }
        }
        return false
    }

    /// One local mutation/persistence operation for the complete batch.
    private func setBatchRead(_ ids: Set<String>, at date: Date?) {
        var updated = savedNotifications
        for i in updated.indices where ids.contains(updated[i].id) { updated[i].readAt = date }
        savedNotifications = updated
        for (id, var item) in details where ids.contains(id) { item.readAt = date; details[id] = item }
        changed(Array(ids), persist: false)
    }

    func loadMore() async {
        guard nextCursor != nil else { return }
        displayLimit += 50
    }

    /// Shared local history is the sole unread authority, including while offline.
    func refreshCounts() async {
        await hydrateInbox()
        await reloadSharedInbox()
        updateBadge()
    }

    private func reloadSharedInbox() async {
        guard !isChangingServer, let scope = inboxScope, hydratedScope == scope else { return }
        let generation = sessionGeneration, revision = mutationRevision
        do {
            let items = try await inboxStore.load(scope: scope) ?? []
            guard generation == sessionGeneration, !isChangingServer else { return }
            var merged = Dictionary(items.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
            // Keep unsaved online content visible with its storage warning until
            // the dirty patch succeeds. Tombstones remove older disk rows too.
            for (id, change) in pendingInboxChanges { merged[id] = change.item }
            for (id, changedAt) in itemRevisions where changedAt > revision { merged[id] = notification(id: id) }
            savedNotifications = ordered(Array(merged.values))
            let present = Set(savedNotifications.map(\.id))
            details = details.filter { present.contains($0.key) }
            counts = InboxCounts(unread: savedNotifications.filter(\.isUnread).count,
                                 pending: savedNotifications.filter { $0.question?.isOpen() == true }.count)
            await waitForInboxGrouping()
        } catch {
            guard generation == sessionGeneration else { return }
            inboxStorageError = String(localized: "Couldn't read saved messages. (\(error.localizedDescription))")
        }
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
            notification.readAt = self.notification(id: id)?.readAt
            if notification.question?.status == .pending, let previous = self.notification(id: id)?.question,
               previous.status != .pending { notification.question = previous }
            upsert(notification)
            await reloadSharedInbox()
            await waitForInboxGrouping()
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
            let matches: (MeowNotification) -> Bool = { n in
                if let id { return n.id == id }
                return interval?.contains(n.createdAt) == true
            }
            let removed = Set((savedNotifications + Array(details.values)).filter(matches).map(\.id))
            removeDeletedMessages(removed)
            await waitForInboxGrouping()
        } catch {
            guard generation == sessionGeneration, !isChangingServer, !Self.isCancellation(error) else { return }
            alert = String(localized: "Couldn't delete messages. \(Self.describe(error))")
        }
    }

    private func removeDeletedMessages(_ removed: Set<String>) {
        // Reject in-flight pages/details even when none of their IDs was loaded yet.
        deletionRevision += 1
        refreshTask?.cancel(); refreshTask = nil; refreshTaskID = nil
        loadGeneration += 1
        isLoading = false
        savedNotifications.removeAll { removed.contains($0.id) }
        details = details.filter { !removed.contains($0.key) }
        for id in removed { readRequests[id] = nil }
        inboxPath.removeAll { removed.contains($0) }
        changed(Array(removed))
        counts = InboxCounts(unread: savedNotifications.filter(\.isUnread).count,
                             pending: savedNotifications.filter { $0.question?.isOpen() == true }.count)
        updateBadge()
    }

    @discardableResult
    func markRead(_ id: String, reportFailure: Bool = true) async -> Bool {
        await readLocally([id], reportFailure: reportFailure)
    }

    func markAllRead() async {
        guard !isChangingServer, !isMarkingAllRead, !isMarkingGroupRead else { return }
        isMarkingAllRead = true
        let generation = sessionGeneration
        defer { if generation == sessionGeneration { isMarkingAllRead = false } }
        await hydrateInbox()
        _ = await readLocally(Set(savedNotifications.map(\.id)), reportFailure: true)
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

    @ObservationIgnored private var badgeRevision = 0
    private func updateBadge() {
        guard let scope = inboxScope else { return }
        let generation = sessionGeneration
        badgeRevision += 1
        let revision = badgeRevision
        Task { [weak self] in
            guard let self, let unread = try? await inboxStore.unreadCount(scope: scope),
                  generation == sessionGeneration, revision == badgeRevision, !isChangingServer else { return }
            try? await UNUserNotificationCenter.current().setBadgeCount(unread)
        }
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
