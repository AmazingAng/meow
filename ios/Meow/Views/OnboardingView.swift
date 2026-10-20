import SwiftUI
import UIKit

/// One straight path: server → notifications → connect your computer → test received.
struct OnboardingView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.dynamicTypeSize) private var typeSize
    @ScaledMetric(relativeTo: .largeTitle) private var brandSize = 46
    @State private var step = Step.server
    @State private var serverText = ""
    @State private var serverProblem: AppModel.ServerProblem?
    @State private var isCheckingServer = false
    @State private var isSendingTest = false
    @State private var help: SetupHelpTopic?
    @State private var showServer = false
    private var received: MeowNotification? { model.onboardingNotification }
    @FocusState private var serverFocused: Bool

    enum Step: Int {
        case server = 1, notifications, connect, test
        var title: LocalizedStringKey {
            switch self {
            case .server: "Server"
            case .notifications: "Notifications"
            case .connect: "Connect your computer"
            case .test: "Test connection"
            }
        }
    }

    var body: some View {
        ZStack {
            LinearGradient(
                colors: [Color.accentColor.opacity(0.12), Color(.systemBackground)],
                startPoint: .top, endPoint: .center
            )
            .ignoresSafeArea()

            VStack(spacing: 0) {
                progressHeader
                Group {
                    switch step {
                    case .server: serverStep
                    case .notifications: notifications
                    case .connect: connectStep
                    case .test: testStep
                    }
                }
                .transition(.opacity)
            }
        }
        .animation(reduceMotion ? nil : .smooth, value: step)
        .sheet(item: $help) { SetupHelpView(topic: $0) }
        .sheet(isPresented: $showServer) { ServerSheet() }
        .onChange(of: model.isRegistered) { _, registered in
            if !registered {
                step = .server
                serverText = model.server?.absoluteString ?? ""
            }
        }
        .onAppear {
            serverText = model.server?.absoluteString ?? ""
            if model.isRegistered {
                step = model.permission == .notDetermined ? .notifications : .connect
            }
        }
    }

    private var progressHeader: some View {
        VStack(alignment: .leading, spacing: 8) {
            if step != .server {
                Button {
                    switch step {
                    case .notifications: showServer = true
                    case .connect: step = .notifications
                    case .test: step = .connect
                    case .server: break
                    }
                } label: {
                    Label(step == .notifications ? LocalizedStringKey("Change server…") : LocalizedStringKey("Back"),
                          systemImage: "chevron.left")
                        .frame(minHeight: 44)
                }
                .disabled(isBusy || model.isChangingServer)
                .accessibilityIdentifier("onboarding.back")
            }
            Text("Step \(step.rawValue) of 4").font(.caption).foregroundStyle(.secondary)
            Text(step.title).font(.subheadline.weight(.semibold))
            ProgressView(value: Double(step.rawValue), total: 4)
                .accessibilityLabel(Text(step.title))
        }
        .padding(.horizontal, 24)
        .padding(.vertical, 8)
        .frame(maxWidth: 640, alignment: .leading)
    }

    // MARK: Server

    private var serverStep: some View {
        page {
            Image("Mascot")
                .resizable()
                .scaledToFit()
                .frame(maxWidth: typeSize.isAccessibilitySize ? 120 : 180)
                .accessibilityHidden(true)
            VStack(spacing: 12) {
                Text(verbatim: "Meow")
                    .font(.system(size: brandSize, weight: .heavy, design: .rounded))
                    .foregroundStyle(Color.accentColor)
                Text("Your agents are calling.")
                    .font(.title3.weight(.semibold))
                Text("Push notifications and quick replies for Claude Code, Codex and your scripts.")
                    .foregroundStyle(.secondary)
            }
            VStack(alignment: .leading, spacing: 8) {
                Text("Server URL").font(.subheadline.weight(.semibold))
                TextField(text: $serverText, prompt: Text(verbatim: "https://meow.example.com")) {
                    Text("Server URL")
                }
                .focused($serverFocused)
                .keyboardType(.URL)
                .textContentType(.URL)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .submitLabel(.go)
                .accessibilityIdentifier("server.address")
                .onSubmit(start)
                .onChange(of: serverText) { serverProblem = nil }
                .padding(12)
                .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                .disabled(isCheckingServer || model.isRegistering)
                if let serverProblem {
                    Label(serverProblem.message, systemImage: "exclamationmark.circle")
                        .font(.footnote)
                        .foregroundStyle(.red)
                } else {
                    Text("The address of your Meow server. You can change it later in Settings.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
            }
            .multilineTextAlignment(.leading)
            Button("Where do I get a server address?") { help = .server }
                .frame(minHeight: 44)
                .accessibilityIdentifier("onboarding.serverHelp")
        } footer: {
            Button(action: start) {
                ZStack {
                    Text("Get started").opacity(isBusy ? 0 : 1)
                    if isBusy { ProgressView().tint(.white) }
                }
                .frame(maxWidth: .infinity, minHeight: 24)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .disabled(isBusy || model.isChangingServer
                      || serverText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        }
    }

    private var isBusy: Bool { isCheckingServer || model.isRegistering }

    /// Checks the server before registering, so a typo gets a specific answer.
    private func start() {
        guard !isBusy else { return }
        serverFocused = false
        isCheckingServer = true
        Task {
            let result = await AppModel.checkServer(serverText)
            isCheckingServer = false
            switch result {
            case .failure(let problem):
                serverProblem = problem
            case .success(let url):
                model.setServer(url)
                await model.register()
                if model.isRegistered { step = .notifications }
            }
        }
    }

    // MARK: Notifications

    private var notifications: some View {
        page {
            Image(systemName: model.permission == .denied ? "bell.slash.fill" : "bell.badge.fill")
                .font(.system(size: 64))
                .symbolRenderingMode(.hierarchical)
                .foregroundStyle(Color.accentColor)
                .accessibilityHidden(true)
            VStack(spacing: 12) {
                if model.permission == .denied {
                    Text("Notifications are off").font(.title2.bold())
                    Text("Turn them on in Settings so Meow can alert you. Without them, messages still arrive in the inbox.")
                        .foregroundStyle(.secondary)
                } else {
                    Text("Turn on notifications").font(.title2.bold())
                    Text("Meow tells you when an agent finishes, fails or needs an answer, and you can reply right from the notification.")
                        .foregroundStyle(.secondary)
                }
            }
        } footer: {
            if model.permission == .denied {
                Button {
                    if let url = URL(string: UIApplication.openSettingsURLString) {
                        UIApplication.shared.open(url)
                    }
                } label: {
                    Text("Open Settings").frame(maxWidth: .infinity, minHeight: 24)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                Button("Continue") { step = .connect }
                    .frame(maxWidth: .infinity, minHeight: 44)
            } else {
                Button {
                    Task {
                        await model.requestNotificationPermission()
                        // A refusal stays here to offer Settings instead of a dead end.
                        if model.permission != .denied { step = .connect }
                    }
                } label: {
                    Text("Allow notifications").frame(maxWidth: .infinity, minHeight: 24)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                Button("Not now") { step = .connect }
                    .frame(maxWidth: .infinity, minHeight: 44)
            }
        }
        .onChange(of: model.permission) {
            // Back from Settings with notifications on.
            if step == .notifications, model.isNotificationPermissionGranted { step = .connect }
        }
    }

    // MARK: Connect

    private var connectStep: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                VStack(alignment: .leading, spacing: 12) {
                    Image("LogoBlue")
                        .resizable()
                        .scaledToFit()
                        .frame(width: 48)
                        .accessibilityHidden(true)
                    Text("Connect your computer").font(.largeTitle.bold())
                    Text("Run this command in your computer's terminal. It installs Meow, asks for your Device Key and connects the agents it finds. No Go or GitHub account is needed.")
                        .foregroundStyle(.secondary)
                }
                Button { help = .cli } label: {
                    Label("Homebrew and other installation options", systemImage: "arrow.down.circle")
                        .frame(minHeight: 44)
                }
                .accessibilityIdentifier("onboarding.cliHelp")
                CommandBlock(title: "1. Install and connect", command: model.installAndSetupCommand, copyTitle: "Copy install and setup command")
                if let key = model.deviceKey {
                    VStack(alignment: .leading, spacing: 8) {
                        CommandBlock(title: "2. Paste your Device Key when asked", command: key, copyTitle: "Copy Device Key")
                        Text("Agents and scripts use your Device Key to send to this iPhone. The key can only send, never read or answer.")
                            .font(.footnote)
                            .foregroundStyle(.secondary)
                    }
                    DisclosureGroup {
                        VStack(alignment: .leading, spacing: 20) {
                            CommandBlock(title: "CLI already installed", command: model.setupCommand, copyTitle: "Copy setup command")
                            CommandBlock(title: "Log in with the Meow CLI", command: model.loginCommand(for: key))
                            CommandBlock(title: "Or send with curl", command: model.curlCommand(for: key))
                        }
                        .padding(.top, 12)
                    } label: {
                        Text("Advanced").font(.subheadline.weight(.semibold))
                    }
                }
            }
            .frame(maxWidth: 640, alignment: .leading)
            .padding(24)
            .frame(maxWidth: .infinity)
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            Button { step = .test } label: {
                Text("Next").frame(maxWidth: .infinity, minHeight: 24)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .frame(maxWidth: 640)
            .padding(.horizontal, 24)
            .padding(.vertical, 12)
            .frame(maxWidth: .infinity)
            .background(.bar)
        }
    }

    // MARK: Test

    private var testStep: some View {
        page {
            if let received {
                Image(systemName: "checkmark.circle.fill")
                    .font(.system(size: 64))
                    .symbolRenderingMode(.hierarchical)
                    .foregroundStyle(.green)
                    .accessibilityHidden(true)
                VStack(spacing: 12) {
                    Text("Connected — received “\(received.title.isEmpty ? received.body : received.title)”")
                        .font(.title2.bold())
                    Text("Your agents can reach you now.")
                        .foregroundStyle(.secondary)
                }
            } else {
                ProgressView()
                    .controlSize(.large)
                VStack(spacing: 12) {
                    Text("Waiting for a test notification from your computer…")
                        .font(.title2.bold())
                    Text("meow setup sends one when it finishes. You can also run meow \"Hello\" in your terminal.")
                        .foregroundStyle(.secondary)
                }
            }
        } footer: {
            if received != nil {
                Button { Task { await model.finishOnboarding() } } label: {
                    Text("Done").frame(maxWidth: .infinity, minHeight: 24)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
            } else {
                Button {
                    Task {
                        isSendingTest = true
                        await model.sendTestNotification()
                        isSendingTest = false
                    }
                } label: {
                    HStack {
                        Label("Send a test from this iPhone", systemImage: "paperplane")
                        if isSendingTest { ProgressView() }
                    }
                    .frame(maxWidth: .infinity, minHeight: 24)
                }
                .buttonStyle(.bordered)
                .controlSize(.large)
                .disabled(isSendingTest)
                Text("This checks the server, not your computer.")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                Button("Skip for now") { Task { await model.finishOnboarding() } }
                    .frame(maxWidth: .infinity, minHeight: 44)
            }
        }
        // Check the saved inbox immediately on entry, then synchronize while
        // waiting. There is no baseline fetch that can swallow an early test.
        .task {
            while !Task.isCancelled, received == nil {
                await model.refresh()
                do { try await Task.sleep(for: .seconds(2)) }
                catch { return }
            }
        }
        .onChange(of: received?.id, initial: true) {
            guard let received else { return }
            UIAccessibility.post(notification: .announcement,
                argument: String(localized: "Connected — received “\(received.title.isEmpty ? received.body : received.title)”"))
        }
    }

    /// Scroll the explanation independently while keeping the next action in
    /// the safe area, including on small screens and with accessibility text.
    private func page<Content: View, Footer: View>(
        @ViewBuilder content: @escaping () -> Content, @ViewBuilder footer: @escaping () -> Footer
    ) -> some View {
        GeometryReader { geometry in
            ScrollView {
                VStack(spacing: 24, content: content)
                    .multilineTextAlignment(.center)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: 480)
                    .padding(24)
                    .frame(maxWidth: .infinity, minHeight: geometry.size.height)
            }
            .scrollIndicators(.hidden)
            .scrollDismissesKeyboard(.interactively)
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            VStack(spacing: 8, content: footer)
                .frame(maxWidth: 480)
                .padding(.horizontal, 24)
                .padding(.vertical, 12)
                .frame(maxWidth: .infinity)
                .background(.bar)
        }
    }
}
