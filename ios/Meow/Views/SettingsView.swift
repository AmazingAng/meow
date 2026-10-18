import SwiftUI
import UIKit

struct SettingsView: View {
    @Environment(AppModel.self) private var model
    @State private var confirmRotate = false
    @State private var showServer = false
    @State private var isSendingTest = false

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    HStack(spacing: 14) {
                        RoundedRectangle(cornerRadius: 14, style: .continuous)
                            .fill(Color.accentColor.gradient)
                            .frame(width: 52, height: 52)
                            .overlay(Image("LogoWhite").resizable().scaledToFit().padding(9))
                            .accessibilityHidden(true)
                        VStack(alignment: .leading, spacing: 4) {
                            Text(verbatim: "Meow").font(.title3.bold())
                            Text("Push notifications for AI agents.")
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                        }
                    }
                    .padding(.vertical, 4)
                }

                Section {
                    LabeledContent("Server") {
                        Text(verbatim: model.serverDisplay)
                            .lineLimit(2)
                            .truncationMode(.middle)
                    }
                    Button { showServer = true } label: {
                        Label("Change server…", systemImage: "network")
                    }
                } header: {
                    Text("Connection")
                } footer: {
                    Text("Use the hosted service or connect to your own Meow server.")
                }

                notificationSection
                deviceSection

                Section {
                    Button("Create new Device Key…", role: .destructive) { confirmRotate = true }
                } header: {
                    Text("Security")
                } footer: {
                    Text("Creating a new key immediately disables the current key.")
                }

                Section("About") {
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
                CopyButton(title: "Copy CLI login command", text: model.loginCommand(for: key))
                Button {
                    Task {
                        isSendingTest = true
                        await model.sendTestNotification()
                        isSendingTest = false
                    }
                } label: {
                    HStack {
                        Label("Send a test notification", systemImage: "paperplane")
                        if isSendingTest { Spacer(); ProgressView() }
                    }
                }
                .disabled(isSendingTest)
            }
        } header: {
            Text("Device")
        } footer: {
            Text("Agents use the Device Key to send to this iPhone. It can only send, never read or answer.")
        }
    }

    private var notificationSection: some View {
        Section("Notifications") {
            LabeledContent("Permission") { Text(permission) }
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
            if model.isNotificationPermissionGranted {
                LabeledContent("Push") {
                    if model.hasPushToken {
                        Label("Ready", systemImage: "checkmark.circle")
                    } else if let error = model.pushRegistrationError {
                        Text(error)
                    } else {
                        Text("Connecting…")
                    }
                }
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

    private var version: String {
        let info = Bundle.main.infoDictionary
        let short = info?["CFBundleShortVersionString"] as? String ?? "?"
        let build = info?["CFBundleVersion"] as? String ?? "?"
        return "\(short) (\(build))"
    }
}

struct ServerSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var text = ""
    @State private var invalid = false
    @State private var confirmSwitch = false
    @State private var switchError: String?
    @State private var pendingReplyCount = 0
    @State private var isChecking = false
    @FocusState private var serverFocused: Bool

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField(text: $text, prompt: Text(verbatim: MeowShared.defaultServer.absoluteString)) {
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
                    .onChange(of: text) { invalid = false }
                } header: {
                    Text("Server URL")
                } footer: {
                    if invalid {
                        Label("Enter a full address such as https://meow.example.com.", systemImage: "exclamationmark.circle")
                            .foregroundStyle(.red)
                    } else {
                        Text("Self-hosting Meow? Enter your server's address.")
                    }
                }
                if parsed != MeowShared.defaultServer {
                    Section {
                        Button("Use the hosted Meow server") {
                            text = MeowShared.defaultServer.absoluteString
                        }
                    }
                }
                if model.isRegistered {
                    Section {
                        Text("Switching servers requires a new registration and Device Key. Saved replies are kept unless you choose to discard them.")
                            .font(.subheadline)
                            .foregroundStyle(.secondary)
                    }
                }
            }
            .scrollDismissesKeyboard(.interactively)
            .navigationTitle("Server")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .disabled(model.isChangingServer)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save", action: save)
                        .disabled(model.isRegistering || model.isChangingServer || isChecking)
                }
            }
            .alert(switchTitle, isPresented: $confirmSwitch) {
                Button("Cancel", role: .cancel) {}
                if pendingReplyCount > 0 {
                    Button("Discard and switch", role: .destructive) { switchServer(discard: true) }
                } else {
                    Button("Switch server", role: .destructive) { switchServer(discard: false) }
                }
            } message: {
                if pendingReplyCount > 0 {
                    Text("Saved replies to discard: \(pendingReplyCount). You'll register on the new server. Answers already processed cannot be undone.")
                } else {
                    Text("You'll register on the new server with a new Device Key. Update your agents to use it.")
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
        .onAppear { text = model.server.absoluteString }
    }

    private var switchTitle: LocalizedStringKey {
        pendingReplyCount > 0 ? "Discard saved replies?" : "Switch servers?"
    }

    private var parsed: URL? {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard var c = URLComponents(string: trimmed),
              let scheme = c.scheme?.lowercased(), scheme == "https" || scheme == "http",
              let host = c.host, !host.isEmpty
        else { return nil }
        while c.path.hasSuffix("/") { c.path.removeLast() }
        c.query = nil
        c.fragment = nil
        return c.url
    }

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
            invalid = true
            return
        }
        if url == model.server {
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
