import SwiftUI

struct OnboardingView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.dynamicTypeSize) private var typeSize
    @ScaledMetric(relativeTo: .largeTitle) private var brandSize = 46
    @State private var step = Step.welcome
    @State private var showServer = false
    @State private var isSendingTest = false

    enum Step { case welcome, notifications, key }

    var body: some View {
        ZStack {
            LinearGradient(
                colors: [Color.accentColor.opacity(0.12), Color(.systemBackground)],
                startPoint: .top, endPoint: .center
            )
            .ignoresSafeArea()

            Group {
                switch step {
                case .welcome: welcome
                case .notifications: notifications
                case .key: keyStep
                }
            }
            .transition(.opacity)
        }
        .animation(reduceMotion ? nil : .smooth, value: step)
        .sheet(isPresented: $showServer) { ServerSheet() }
        .onAppear {
            if model.isRegistered {
                step = model.permission == .notDetermined ? .notifications : .key
            }
        }
    }

    private var welcome: some View {
        page {
            Image("Mascot")
                .resizable()
                .scaledToFit()
                .frame(maxWidth: typeSize.isAccessibilitySize ? 140 : 220)
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
        } footer: {
            Button {
                Task {
                    await model.register()
                    if model.isRegistered { step = .notifications }
                }
            } label: {
                ZStack {
                    Text("Get started").opacity(model.isRegistering ? 0 : 1)
                    if model.isRegistering { ProgressView().tint(.white) }
                }
                .frame(maxWidth: .infinity, minHeight: 24)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .disabled(model.isRegistering)

            Button { showServer = true } label: {
                Text("Server: \(model.serverDisplay)")
                    .font(.footnote)
                    .frame(maxWidth: .infinity, minHeight: 44)
            }
            .disabled(model.isRegistering || model.isChangingServer)
        }
    }

    private var notifications: some View {
        page {
            Image(systemName: "bell.badge.fill")
                .font(.system(size: 64))
                .symbolRenderingMode(.hierarchical)
                .foregroundStyle(Color.accentColor)
                .accessibilityHidden(true)
            VStack(spacing: 12) {
                Text("Turn on notifications").font(.title2.bold())
                Text("Meow tells you when an agent finishes, fails or needs an answer, and you can reply right from the notification.")
                    .foregroundStyle(.secondary)
            }
        } footer: {
            Button {
                Task {
                    await model.requestNotificationPermission()
                    step = .key
                }
            } label: {
                Text("Allow notifications").frame(maxWidth: .infinity, minHeight: 24)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            Button("Not now") { step = .key }
                .frame(maxWidth: .infinity, minHeight: 44)
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

    private var keyStep: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                VStack(alignment: .leading, spacing: 12) {
                    Image("LogoBlue")
                        .resizable()
                        .scaledToFit()
                        .frame(width: 48)
                        .accessibilityHidden(true)
                    Text("Connect your agents").font(.largeTitle.bold())
                    Text("Agents and scripts use your Device Key to send to this iPhone. The key can only send, never read or answer.")
                        .foregroundStyle(.secondary)
                }
                if let key = model.deviceKey {
                    CommandBlock(title: "Device Key", command: key)
                    CommandBlock(title: "Log in with the Meow CLI", command: model.loginCommand(for: key))
                    CommandBlock(title: "Or send with curl", command: model.curlCommand(for: key))
                }
            }
            .frame(maxWidth: 640, alignment: .leading)
            .padding(24)
            .frame(maxWidth: .infinity)
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            VStack(spacing: 10) {
                Button {
                    Task {
                        isSendingTest = true
                        await model.sendTestNotification()
                        isSendingTest = false
                    }
                } label: {
                    HStack {
                        Label("Send a test notification", systemImage: "paperplane")
                        if isSendingTest { ProgressView() }
                    }
                    .frame(maxWidth: .infinity, minHeight: 24)
                }
                .buttonStyle(.bordered)
                .controlSize(.large)
                .disabled(isSendingTest)

                Button { Task { await model.finishOnboarding() } } label: {
                    Text("Done").frame(maxWidth: .infinity, minHeight: 24)
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
            }
            .frame(maxWidth: 640)
            .padding(.horizontal, 24)
            .padding(.vertical, 12)
            .frame(maxWidth: .infinity)
            .background(.bar)
        }
    }
}
