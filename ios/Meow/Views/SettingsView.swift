import SwiftUI
import UIKit
import AVFAudio

struct SettingsView: View {
    @Environment(AppModel.self) private var model
    @State private var confirmRotate = false
    @State private var showServer = false
    @State private var showReregister = false
    @State private var isSendingTest = false
    @State private var isSendingTimeSensitiveTest = false
    @State private var soundPlayer: AVAudioPlayer?
    @State private var showCLIHelp = false

    var body: some View {
        NavigationStack {
            Form {
                connectionStatus
                if !model.failedReplies.isEmpty || !model.queuedReplies.isEmpty {
                    Section {
                        NavigationLink {
                            FailedRepliesView()
                        } label: {
                            Label("Saved reply records", systemImage: "archivebox")
                                .badge(model.failedReplies.count + model.queuedReplies.count)
                        }
                    }
                }
                notificationSection
                focusSection
                deviceSection
                Section("Server") {
                    Button { showServer = true } label: {
                        Label("Change server…", systemImage: "network")
                    }
                    Button { showReregister = true } label: {
                        Label("Re-register this server…", systemImage: "arrow.clockwise")
                    }
                    .accessibilityIdentifier("server.reregister")
                }

                Section {
                    Button("Create new Device Key…", role: .destructive) { confirmRotate = true }
                } header: {
                    Text("Security")
                } footer: {
                    Text("Creating a new key immediately disables the current key.")
                }

                Section("About") {
                    Label {
                        Text("Push notifications for AI agents.").font(.subheadline)
                    } icon: {
                        Image("LogoBlue").resizable().scaledToFit().frame(width: 24, height: 24)
                    }
                    LabeledContent("Version") { Text(verbatim: version) }
                }
            }
            .navigationTitle("Settings")
            .alert("Create a new Device Key?", isPresented: $confirmRotate) {
                Button("Cancel", role: .cancel) {}
                Button("Create new key", role: .destructive) {
                    Task { await model.rotateKey() }
                }
            } message: {
                Text("The current key will stop working. Update your agents with the new key.")
            }
            .sheet(isPresented: $showServer) { ServerSheet() }
            .sheet(isPresented: $showReregister) { ServerSheet(reregister: true) }
            .sheet(isPresented: $showCLIHelp) { SetupHelpView(topic: .cli) }
        }
    }

    private var deviceSection: some View {
        Section {
            if let key = model.deviceKey {
                LabeledContent("Device Key") {
                    Text(verbatim: "\(key.prefix(9))…\(key.suffix(4))")
                        .font(.body.monospaced())
                        .lineLimit(1)
                }
                CopyButton(title: "Copy Device Key", text: key)
                CopyButton(title: "Copy install and setup command", text: model.installAndSetupCommand)
                CopyButton(title: "Copy setup command", text: model.setupCommand)
                Button("CLI installation help") { showCLIHelp = true }
            }
        } header: {
            Text("Device")
        } footer: {
            Text("Agents use the Device Key to send to this iPhone. It can only send, never read or answer.")
        }
    }

    private var connectionStatus: some View {
        Section {
            LabeledContent("Server") {
                Text(verbatim: model.serverDisplay)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
            }
            LabeledContent("Inbox sync") {
                if model.isLoading {
                    ProgressView().accessibilityLabel("Checking connection")
                } else if model.loadError != nil {
                    Text("Needs attention")
                        .foregroundStyle(.orange)
                } else if model.hasLoaded {
                    Text("Last sync succeeded")
                } else {
                    Text("Not checked")
                }
            }
            .fixedSize(horizontal: false, vertical: true)
            .accessibilityIdentifier("connection.sync")
            if let error = model.loadError {
                Text(error).font(.footnote).foregroundStyle(.secondary)
            }
            LabeledContent("Notification permission") { Text(permission) }
            LabeledContent("Push registration") {
                if !model.isNotificationPermissionGranted {
                    Text("Allow notifications first")
                } else if model.hasPushToken {
                    Text("Ready")
                } else if model.pushRegistrationError != nil {
                    Text("Needs attention")
                        .foregroundStyle(.orange)
                } else {
                    Text("Connecting…")
                }
            }
            .fixedSize(horizontal: false, vertical: true)
            if let error = model.pushRegistrationError {
                Text(error).font(.footnote).foregroundStyle(.secondary)
            }
            Button("Check connection") {
                Task {
                    await model.refresh()
                    await model.refreshPermission()
                    if model.isNotificationPermissionGranted { await model.configurePush() }
                }
            }
            .disabled(model.isLoading || model.isConfiguringPush)
            .accessibilityIdentifier("connection.check")
        } header: {
            Text("Connection & notifications")
        } footer: {
            Text("Push registration alone doesn't confirm delivery. Send a test to check that a notification reaches this iPhone.")
        }
    }

    private var notificationSection: some View {
        Section("Notifications") {
            if model.permission == .notDetermined {
                Button { Task { await model.requestNotificationPermission() } } label: {
                    Label("Allow notifications", systemImage: "bell.badge")
                }
            } else {
                Button {
                    if let url = URL(string: UIApplication.openNotificationSettingsURLString) {
                        UIApplication.shared.open(url)
                    }
                } label: {
                    Label("Open notification settings", systemImage: "arrow.up.forward.app")
                }
            }
            Button {
                isSendingTest = true
                Task {
                    await model.sendTestNotification()
                    isSendingTest = false
                }
            } label: {
                HStack {
                    Label("Send a test notification", systemImage: "paperplane")
                    if isSendingTest { Spacer(); ProgressView() }
                }
            }
            .disabled(isSendingTest || model.deviceKey == nil)
            Button { previewSound() } label: {
                LabeledContent {
                    Text(verbatim: "Meow")
                } label: {
                    Label("Notification sound", systemImage: "speaker.wave.2")
                }
            }
            .accessibilityIdentifier("notifications.previewSound")
            if let gateway = model.gatewayURL {
                DisclosureGroup("Gateway") {
                    Text(verbatim: gateway.absoluteString).textSelection(.enabled)
                }
            }
            if model.isNotificationPermissionGranted, !model.hasPushToken {
                Button("Retry push registration") {
                    Task {
                        await model.refreshPermission()
                        await model.configurePush()
                    }
                }
                .disabled(model.isConfiguringPush)
            }
        }
    }

    private var permission: LocalizedStringKey {
        switch model.permission {
        case .authorized, .provisional, .ephemeral: "On"
        case .denied: "Off"
        default: "Not set"
        }
    }

    private func previewSound() {
        guard let url = Bundle.main.url(forResource: NotificationSound.filename, withExtension: nil) else {
            model.alert = String(localized: "The notification sound is unavailable.")
            return
        }
        do {
            soundPlayer = try AVAudioPlayer(contentsOf: url)
            soundPlayer?.play()
        } catch {
            model.alert = AppModel.describe(error)
        }
    }

    private var focusSection: some View {
        Section {
            LabeledContent("Time Sensitive Notifications") {
                switch model.timeSensitiveSetting {
                case .enabled: Text("On")
                case .disabled: Text("Off")
                default: Text("Unavailable")
                }
            }
            Button {
                Task {
                    isSendingTimeSensitiveTest = true
                    await model.sendTestNotification(priority: "high")
                    isSendingTimeSensitiveTest = false
                }
            } label: {
                HStack {
                    Label("Send a time-sensitive test", systemImage: "bell.badge")
                    if isSendingTimeSensitiveTest { Spacer(); ProgressView() }
                }
            }
            .disabled(isSendingTimeSensitiveTest || !model.isNotificationPermissionGranted)
        } header: {
            Text("During Focus")
        } footer: {
            Text("High-priority messages and questions use Time Sensitive Notifications. Enable them in Meow's notification settings and in Settings → Focus → your Focus → Apps. Silent mode still silences their sound.")
        }
    }

    private var version: String {
        let info = Bundle.main.infoDictionary
        let short = info?["CFBundleShortVersionString"] as? String ?? "?"
        let build = info?["CFBundleVersion"] as? String ?? "?"
        return "\(short) (\(build))"
    }
}

struct ServerSheet: View {
    var reregister = false
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var text = ""
    @State private var problem: AppModel.ServerProblem?
    @State private var confirmSwitch = false
    @State private var switchError: String?
    @State private var pendingReplyCount = 0
    @State private var isChecking = false
    @FocusState private var serverFocused: Bool

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField(text: $text, prompt: Text(verbatim: "https://meow.example.com")) {
                        Text("Server URL")
                    }
                    .focused($serverFocused)
                    .keyboardType(.URL)
                    .textContentType(.URL)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .submitLabel(.go)
                    .accessibilityIdentifier("server.address")
                    .onSubmit(save)
                    .onChange(of: text) { problem = nil }
                    .disabled(reregister)
                } header: {
                    Text("Server URL")
                } footer: {
                    if let problem {
                        Label(problem.message, systemImage: "exclamationmark.circle")
                            .foregroundStyle(.red)
                    } else {
                        Text("Self-hosting Meow? Enter your server's address.")
                    }
                }
                if !reregister, let suggested = MeowShared.defaultServer, parsed != suggested {
                    Section {
                        Button("Use the hosted Meow server") {
                            text = suggested.absoluteString
                        }
                    }
                }
                if model.isRegistered {
                    Section {
                        Text(reregister
                             ? LocalizedStringKey("Re-registering creates a new Device Key. Your previous inbox won't be available in the new registration. Saved replies are kept unless you choose to discard them.")
                             : LocalizedStringKey("Switching servers requires a new registration and Device Key. Saved replies are kept unless you choose to discard them."))
                            .font(.subheadline)
                            .foregroundStyle(.secondary)
                    }
                }
            }
            .scrollDismissesKeyboard(.interactively)
            .navigationTitle(reregister ? LocalizedStringKey("Re-register") : LocalizedStringKey("Server"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .disabled(model.isChangingServer)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button(reregister ? LocalizedStringKey("Continue") : LocalizedStringKey("Save"), action: save)
                        .disabled(model.isRegistering || model.isChangingServer || isChecking)
                }
            }
            .alert(switchTitle, isPresented: $confirmSwitch) {
                Button("Cancel", role: .cancel) {}
                if pendingReplyCount > 0 {
                    Button(reregister ? LocalizedStringKey("Discard and re-register") : LocalizedStringKey("Discard and switch"), role: .destructive) { switchServer(discard: true) }
                } else {
                    Button(reregister ? LocalizedStringKey("Re-register") : LocalizedStringKey("Switch server"), role: .destructive) { switchServer(discard: false) }
                }
            } message: {
                if pendingReplyCount > 0 {
                    if reregister {
                        Text("Saved replies to discard: \(pendingReplyCount). You'll register again on this server with a new Device Key. Answers already processed cannot be undone.")
                    } else {
                        Text("Saved replies to discard: \(pendingReplyCount). You'll register on the new server. Answers already processed cannot be undone.")
                    }
                } else {
                    Text(reregister
                         ? LocalizedStringKey("You'll register again on this server with a new Device Key. Update your agents to use it.")
                         : LocalizedStringKey("You'll register on the new server with a new Device Key. Update your agents to use it."))
                }
            }
        }
        .presentationDetents(typeSize.isAccessibilitySize ? [.large] : [.medium, .large])
        .presentationDragIndicator(.visible)
        .interactiveDismissDisabled(model.isChangingServer)
        .alert("Can't change server", isPresented: Binding(
            get: { switchError != nil }, set: { if !$0 { switchError = nil } }
        )) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(switchError ?? "")
        }
        .onAppear { text = model.server?.absoluteString ?? "" }
    }

    private var switchTitle: LocalizedStringKey {
        pendingReplyCount > 0 ? "Discard saved replies?" : (reregister ? "Re-register this server?" : "Switch servers?")
    }

    private var parsed: URL? { MeowShared.serverURL(from: text) }

    private func switchServer(discard: Bool) {
        guard let url = parsed else { return }
        Task {
            if await model.reset(server: url, discardPendingReplies: discard) {
                dismiss()
            } else {
                let resetError = model.alert
                model.alert = nil
                let count = await model.pendingReplyCount()
                if !discard, count > 0 {
                    // A reply may have arrived while the first alert was open.
                    pendingReplyCount = count
                    confirmSwitch = true
                } else {
                    switchError = resetError
                }
            }
        }
    }

    private func save() {
        guard !isChecking, !model.isChangingServer, !model.isRegistering else { return }
        serverFocused = false
        guard let url = parsed else {
            problem = .invalidAddress
            return
        }
        if url == model.server && !reregister {
            dismiss()
        } else if model.isRegistered {
            isChecking = true
            Task {
                pendingReplyCount = await model.pendingReplyCount()
                isChecking = false
                confirmSwitch = true
            }
        } else {
            model.setServer(url)
            dismiss()
        }
    }
}
