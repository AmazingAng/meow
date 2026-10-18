import SwiftUI

struct RootView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        Group {
            if model.isRegistered && model.hasFinishedOnboarding {
                MainTabs()
            } else {
                OnboardingView()
            }
        }
        .overlay(alignment: .top) { ToastView() }
        .animation(reduceMotion ? nil : .snappy, value: model.toast)
        .alert(
            "Something went wrong",
            isPresented: Binding(get: { model.alert != nil }, set: { if !$0 { model.alert = nil } })
        ) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(model.alert ?? "")
        }
        .onChange(of: scenePhase, initial: true) { _, phase in
            model.scenePhaseChanged(phase)
        }
    }
}

struct MainTabs: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        TabView(selection: $model.selectedTab) {
            InboxView()
                .tabItem { Label("Inbox", systemImage: "tray.fill") }
                .tag(AppModel.Tab.inbox)
                .badge(model.counts.unread)
            SettingsView()
                .tabItem { Label("Settings", systemImage: "gearshape.fill") }
                .tag(AppModel.Tab.settings)
        }
    }
}

struct ToastView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        if let toast = model.toast {
            Text(toast.message)
                .font(.subheadline.weight(.medium))
                .multilineTextAlignment(.center)
                .padding(.horizontal, 16)
                .padding(.vertical, 10)
                .background(.regularMaterial, in: Capsule())
                .shadow(color: .black.opacity(0.12), radius: 10, y: 3)
                .padding(.horizontal, 24)
                .padding(.top, 8)
                .allowsHitTesting(false)
                .transition(.move(edge: .top).combined(with: .opacity))
                .task(id: toast.id) {
                    try? await Task.sleep(for: .seconds(2.5))
                    model.toast = nil
                }
        }
    }
}
