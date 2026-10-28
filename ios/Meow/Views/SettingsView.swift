import SwiftUI
import UIKit
import AVFAudio

struct SettingsView: View {
    @Environment(AppModel.self) private var model
    @State private var isSendingTest = false
    @State private var isRetrying = false
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var showAgentPrompt = false
    @State private var showFocusHelp = false
    @State private var showServer = false
    @State private var showReregister = false
    @State private var confirmResetKey = false
    @State private var showSavedReplies = false
    @State private var soundPlayer: AVAudioPlayer?
    @AppStorage(MeowShared.notificationSoundKey, store: MeowShared.defaults)
    private var notificationSound = NotificationSoundChoice.meow

    var body: some View {
        NavigationStack {
            Form {
                if model.loadError != nil || model.pushRegistrationError != nil || model.deviceKey == nil {
                    Section {
                        Text("Connection needs attention")
                            .foregroundStyle(.orange)
                            .accessibilityIdentifier("settings.connectionWarning")
                        Text(model.loadError ?? model.pushRegistrationError ?? String(localized: "Your Device Key is unavailable. Re-register this server to reconnect your computer."))
                            .font(.footnote).foregroundStyle(.secondary)
                        Button("Retry") {
                            isRetrying = true
                            Task {
                                await model.refresh()
                                await model.refreshPermission()
                                if model.isNotificationPermissionGranted { await model.configurePush() }
                                isRetrying = false
                            }
                        }
                        .disabled(isRetrying)
                        .accessibilityIdentifier("connection.check")
                        if model.loadError != nil || model.deviceKey == nil {
                            Button("Re-register this server…") { showReregister = true }
                                .accessibilityIdentifier("server.reregister")
                        }
                    }
                }
                Section("Notifications") {
                    Button(action: openNotificationSettings) {
                        LabeledContent {
                            Text(permission).foregroundStyle(.secondary)
                        } label: {
                            Text("Notifications")
                                .foregroundStyle(.primary)
                        }
                    }
                    .accessibilityLabel(model.permission == .notDetermined
                                        ? Text("Allow notifications") : Text("Open notification settings"))
                    .accessibilityIdentifier("settings.notifications")
                    .accessibilityValue(Text(permission))
                    Button { showFocusHelp = true } label: {
                        Text("During Focus")
                    }
                    .accessibilityIdentifier("settings.focusHelp")
                    Picker(selection: $notificationSound) {
                        ForEach(NotificationSoundChoice.allCases) { choice in
                            Text(choice.title).tag(choice)
                        }
                    } label: {
                        Text("Notification sound")
                    }
                    .pickerStyle(.menu)
                    .accessibilityIdentifier("notifications.sound")
                    .onChange(of: notificationSound) {
                        soundPlayer?.stop()
                        if notificationSound == .meow { previewSound() }
                    }
                    Button {
                        isSendingTest = true
                        Task {
                            await model.sendTestNotification()
                            isSendingTest = false
                        }
                    } label: {
                        HStack {
                            Text("Send a test notification")
                            if isSendingTest { Spacer(); ProgressView() }
                        }
                    }
                    .disabled(isSendingTest || model.deviceKey == nil)
                }
                Section {
                    Button { showServer = true } label: {
                        LabeledContent {
                            Text(verbatim: model.serverDisplay).foregroundStyle(.secondary)
                                .fixedSize(horizontal: false, vertical: true)
                        } label: {
                            Text("Server").foregroundStyle(.primary)
                        }
                    }
                    .accessibilityIdentifier("settings.server")
                    if let key = model.deviceKey {
                        CopyButton(title: "Device Key", text: key,
                                   maskedPreview: String(key.prefix(3)) + "…" + String(key.suffix(3)))
                            .accessibilityHint("Tap to copy the complete Device Key")
                            .accessibilityIdentifier("settings.deviceKey")
                    } else {
                        LabeledContent("Device Key", value: String(localized: "Unavailable"))
                            .foregroundStyle(.secondary)
                    }
                    ShareLink(item: model.installAndSetupCommand) {
                        Text("Share setup command")
                    }
                    .accessibilityIdentifier("settings.shareSetup")
                    Button(role: .destructive) { confirmResetKey = true } label: {
                        HStack {
                            Text("Reset Device Key…")
                            if model.isRotatingKey { Spacer(); ProgressView() }
                        }
                    }
                    .disabled(!model.isRegistered || model.isChangingServer || model.isRotatingKey)
                    .accessibilityIdentifier("settings.rotateKey")
                } header: {
                    Text("Connection")
                } footer: {
                    Text("Run the shared command on your computer, then paste your Device Key when asked.")
                }
                if !model.failedReplies.isEmpty || !model.queuedReplies.isEmpty {
                    Section {
                        Button { showSavedReplies = true } label: {
                            Text("Saved reply records")
                        }
                    }
                }
                Section {
                    Link(destination: URL(string: "https://github.com/AmazingAng/meow#installation")!) {
                        Text("Installation and usage")
                    }
                    .accessibilityIdentifier("settings.helpDocs")
                    Link(destination: URL(string: "https://github.com/AmazingAng/meow#http-api")!) {
                        Text("HTTP API and script examples")
                    }
                    .accessibilityIdentifier("settings.helpAPI")
                    Button("Ask your agent to use Meow") { showAgentPrompt = true }
                        .accessibilityIdentifier("settings.showPrompt")
                } header: {
                    Text("Help")
                } footer: {
                    Text(verbatim: "Meow " + version)
                }
            }
            .navigationTitle("Settings")
            .onDisappear { soundPlayer?.stop() }
            .alert("Reset Device Key?", isPresented: $confirmResetKey) {
                Button("Cancel", role: .cancel) {}
                Button("Reset key", role: .destructive) { Task { await model.rotateKey() } }
            } message: {
                Text("Your old Device Key will stop working. Sign in to Meow again on your computer with the new key. Your messages will be kept.")
            }
            .alert("During Focus", isPresented: $showFocusHelp) {
                Button("Open notification settings", action: openSystemNotificationSettings)
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("Turn on Time Sensitive Notifications for Meow. Then go to iPhone Settings → Focus → your Focus → Apps and allow Time Sensitive Notifications. Important alerts can then arrive during Do Not Disturb. Silent mode still silences their sound.")
            }
            .sheet(isPresented: $showAgentPrompt) {
                NavigationStack {
                    ScrollView {
                        Text(verbatim: agentPrompt)
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(24)
                            .accessibilityIdentifier("settings.agentPrompt")
                    }
                    .safeAreaInset(edge: .bottom) {
                        CopyButton(title: "Copy prompt", text: agentPrompt)
                            .labelStyle(.titleOnly)
                            .buttonStyle(.borderedProminent)
                            .accessibilityIdentifier("settings.copyPrompt")
                            .padding()
                            .frame(maxWidth: .infinity)
                            .background(.bar)
                    }
                    .navigationTitle("Agent prompt")
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar {
                        ToolbarItem(placement: .confirmationAction) {
                            Button("Done") { showAgentPrompt = false }
                        }
                    }
                }
                .presentationDetents(typeSize.isAccessibilitySize ? [.large] : [.medium, .large])
                .presentationDragIndicator(.visible)
            }
            .sheet(isPresented: $showServer) { ServerSheet() }
            .sheet(isPresented: $showReregister) { ServerSheet(reregister: true) }
            .sheet(isPresented: $showSavedReplies) {
                NavigationStack {
                    FailedRepliesView()
                        .toolbar {
                            ToolbarItem(placement: .confirmationAction) {
                                Button("Done") { showSavedReplies = false }
                            }
                        }
                }
            }
        }
    }

    private var agentPrompt: String {
        String(localized: "Help me set up and use Meow for this task. Read the official skill at https://github.com/AmazingAng/meow/blob/main/skills/meow/SKILL.md and check the connection with meow doctor. If a Device Key is needed, ask me to paste it into the terminal, not this chat. Notify me when the task finishes, fails, or needs my input; skip routine progress. I will reply on the computer.")
    }

    private var permission: LocalizedStringKey {
        switch model.permission {
        case .authorized, .provisional, .ephemeral: "On"
        case .denied: "Off"
        default: "Not set"
        }
    }

    private func openNotificationSettings() {
        if model.permission == .notDetermined {
            Task { await model.requestNotificationPermission() }
        } else {
            openSystemNotificationSettings()
        }
    }

    private func openSystemNotificationSettings() {
        if let url = URL(string: UIApplication.openNotificationSettingsURLString) {
            UIApplication.shared.open(url)
        }
    }

    private func previewSound() {
        guard let url = Bundle.main.url(forResource: NotificationSound.filename, withExtension: nil) else { return }
        do {
            soundPlayer = try AVAudioPlayer(contentsOf: url)
            soundPlayer?.play()
        } catch {
            model.alert = AppModel.describe(error)
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
