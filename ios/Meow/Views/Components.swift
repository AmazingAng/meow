import LocalAuthentication
import SwiftUI
import UniformTypeIdentifiers

/// Initials and a stable color for a free-text source such as "Claude Code".
enum AvatarStyle {
    static let palette: [Color] = [
        Color(red: 0.93, green: 0.42, blue: 0.24), // orange
        Color(red: 0.20, green: 0.21, blue: 0.24), // graphite
        Color(red: 0.35, green: 0.34, blue: 0.84), // indigo
        Color(red: 0.11, green: 0.60, blue: 0.60), // teal
        Color(red: 0.85, green: 0.27, blue: 0.47), // pink
        Color(red: 0.20, green: 0.62, blue: 0.34), // green
        Color(red: 0.58, green: 0.33, blue: 0.80), // purple
        Color(red: 0.03, green: 0.47, blue: 1.00), // meow blue
    ]

    /// Up to two initials: "Claude Code" → "CC", "codex" → "C", "部署机器人" → "部".
    static func initials(for source: String) -> String {
        source
            .split { $0.isWhitespace || $0 == "-" || $0 == "_" || $0 == "/" || $0 == "." }
            .prefix(2)
            .compactMap(\.first)
            .map { String($0).uppercased() }
            .joined()
    }

    /// A stable index into `palette`; String.hashValue changes between launches.
    static func colorIndex(for source: String) -> Int {
        var hash: UInt32 = 2_166_136_261 // FNV-1a
        for byte in source.lowercased().utf8 {
            hash = (hash ^ UInt32(byte)) &* 16_777_619
        }
        return Int(hash % UInt32(palette.count))
    }
}

struct SourceAvatar: View {
    var source: String
    var size: CGFloat = 40

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: size * 0.28, style: .continuous)
        let initials = AvatarStyle.initials(for: source)
        Group {
            if initials.isEmpty {
                shape.fill(Color.accentColor.gradient)
                    .overlay(Image("LogoWhite").resizable().scaledToFit().padding(size * 0.17))
            } else {
                shape.fill(AvatarStyle.palette[AvatarStyle.colorIndex(for: source)].gradient)
                    .overlay(
                        Text(verbatim: initials)
                            .font(.system(size: size * 0.38, weight: .semibold, design: .rounded))
                            .foregroundStyle(.white)
                            .minimumScaleFactor(0.5)
                    )
            }
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }
}

/// How a question's state is shown in the inbox and on the detail screen.
struct QuestionAppearance {
    var tag: LocalizedStringKey
    var headline: LocalizedStringKey
    var icon: String
    var color: Color

    init(_ q: MeowNotification.Question, now: Date = .now) {
        switch q.status {
        case .pending where q.isOpen(at: now):
            self.init(tag: "Waiting", headline: "Waiting for your answer", icon: "hourglass", color: .orange)
        case .pending, .expired:
            self.init(tag: "Expired", headline: "Expired without an answer", icon: "clock.badge.xmark", color: .secondary)
        case .answered where q.isApproval:
            self.init(tag: "Approved", headline: "Approved", icon: "checkmark.circle.fill", color: .green)
        case .answered:
            self.init(tag: "Answered", headline: "You answered", icon: "arrowshape.turn.up.left.circle.fill", color: .accentColor)
        case .rejected:
            self.init(tag: "Rejected", headline: "Rejected", icon: "xmark.circle.fill", color: .red)
        case .cancelled:
            self.init(tag: "Cancelled", headline: "Cancelled by the agent", icon: "minus.circle.fill", color: .secondary)
        }
    }

    private init(tag: LocalizedStringKey, headline: LocalizedStringKey, icon: String, color: Color) {
        self.tag = tag
        self.headline = headline
        self.icon = icon
        self.color = color
    }
}

struct QuestionStatusTag: View {
    var question: MeowNotification.Question

    var body: some View {
        let look = QuestionAppearance(question)
        Label(look.tag, systemImage: look.icon)
            .font(.caption.weight(.semibold))
            .foregroundStyle(look.color)
            .padding(.horizontal, 8)
            .padding(.vertical, 3)
            .background(look.color.opacity(0.12), in: Capsule())
    }
}

/// "now", "5 min ago", or a date for older items; refreshes itself.
struct RelativeTime: View {
    var date: Date

    var body: some View {
        TimelineView(.periodic(from: .now, by: 30)) { context in
            Text(Self.label(for: date, now: context.date))
        }
    }

    static func label(for date: Date, now: Date) -> String {
        let seconds = now.timeIntervalSince(date)
        if seconds < 60 { return String(localized: "now") }
        if seconds < 7 * 86_400 {
            return date.formatted(.relative(presentation: .numeric, unitsStyle: .abbreviated))
        }
        return date.formatted(.dateTime.month(.abbreviated).day())
    }
}

struct ExpiryCountdown: View {
    var expiresAt: Date

    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { context in
            let remaining = max(0, expiresAt.timeIntervalSince(context.date))
            Label {
                Text("Expires in \(Self.format(remaining))")
            } icon: {
                Image(systemName: "clock")
            }
            .font(.footnote)
            .monospacedDigit()
            .foregroundStyle(remaining < 60 ? Color.orange : Color.secondary)
        }
    }

    static func format(_ seconds: TimeInterval) -> String {
        let d = Duration.seconds(Int(seconds.rounded(.up)))
        return seconds >= 3600
            ? d.formatted(.time(pattern: .hourMinuteSecond))
            : d.formatted(.time(pattern: .minuteSecond))
    }
}

struct CopyButton: View {
    var title: LocalizedStringKey = "Copy"
    var text: String
    @State private var copied = false

    var body: some View {
        Button {
            // The Device Key is a secret: don't leave it on the pasteboard forever.
            UIPasteboard.general.setItems(
                [[UTType.plainText.identifier: text]],
                options: [.expirationDate: Date.now.addingTimeInterval(300)])
            copied = true
            UIAccessibility.post(notification: .announcement, argument: String(localized: "Copied"))
        } label: {
            Label(copied ? "Copied" : title, systemImage: copied ? "checkmark" : "doc.on.doc")
                .frame(minHeight: 44)
        }
        .task(id: copied) {
            guard copied else { return }
            try? await Task.sleep(for: .seconds(1.5))
            copied = false
        }
    }
}

/// A titled, copyable block of monospaced text such as a shell command.
struct CommandBlock: View {
    var title: LocalizedStringKey
    var command: String
    /// Names what is copied, for VoiceOver and UI tests.
    var copyTitle: LocalizedStringKey = "Copy"

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text(title).font(.subheadline.weight(.semibold))
                Spacer()
                CopyButton(title: copyTitle, text: command)
                    .font(.subheadline)
                    .labelStyle(.titleAndIcon)
            }
            Text(verbatim: command)
                .fixedSize(horizontal: false, vertical: true)
                .font(.system(.footnote, design: .monospaced))
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(12)
                .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        }
    }
}

enum DeviceAuth {
    /// Asks for Face ID, Touch ID or the passcode. A device without a passcode
    /// cannot authenticate and is let through, as iOS does for notification
    /// actions that require authentication.
    static func confirm(reason: String) async -> Bool {
        let context = LAContext()
        var error: NSError?
        guard context.canEvaluatePolicy(.deviceOwnerAuthentication, error: &error) else {
            return error?.domain == LAErrorDomain && error?.code == LAError.passcodeNotSet.rawValue
        }
        return (try? await context.evaluatePolicy(.deviceOwnerAuthentication, localizedReason: reason)) ?? false
    }
}
