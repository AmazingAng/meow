import SwiftUI

struct DetailView: View {
    let id: String
    @Environment(AppModel.self) private var model
    @State private var loadFailed = false

    var body: some View {
        Group {
            if let n = model.notification(id: id) {
                content(n)
            } else if loadFailed {
                ContentUnavailableView("Notification not found", systemImage: "tray",
                                       description: Text("It may have been removed by the server's retention policy."))
            } else {
                ProgressView()
            }
        }
        .navigationTitle("Notification")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.hidden, for: .tabBar)
        .task(id: id) {
            loadFailed = !(await model.loadDetail(id))
            model.markRead(id)
        }
    }

    private func content(_ n: MeowNotification) -> some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                HStack(spacing: 12) {
                    SourceAvatar(source: n.source, size: 44)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(n.source.isEmpty ? "Meow" : n.source).font(.headline)
                        Text(n.createdAt.formatted(date: .abbreviated, time: .shortened))
                            .font(.subheadline)
                            .foregroundStyle(.secondary)
                    }
                }
                VStack(alignment: .leading, spacing: 12) {
                    Text(n.title)
                        .font(.title2.bold())
                        .textSelection(.enabled)
                    if !n.body.isEmpty {
                        Text(n.body)
                            .textSelection(.enabled)
                    }
                }
                if let q = n.question {
                    QuestionPanel(question: q)
                }
                Divider()
                DetailsSection(notification: n)
            }
            .frame(maxWidth: 640, alignment: .leading)
            .padding(20)
            .frame(maxWidth: .infinity, alignment: .center)
        }
        .scrollDismissesKeyboard(.interactively)
        .background(Color(.systemGroupedBackground))
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if let q = n.question, q.choices == nil {
                TimelineView(.periodic(from: .now, by: 1)) { context in
                    if q.isOpen(at: context.date), model.queuedReplies[q.id] == nil {
                        ReplyComposer(question: q).id(q.id)
                    }
                }
            }
        }
    }
}

private struct QuestionPanel: View {
    let question: MeowNotification.Question
    @Environment(AppModel.self) private var model
    @State private var sending: String?
    @State private var message: String?
    @State private var expiredLocally = false

    private var isOpen: Bool { !expiredLocally && question.isOpen() }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            if let queued = model.queuedReplies[question.id] {
                queuedNotice(queued)
            } else if isOpen {
                VStack(alignment: .leading, spacing: 6) {
                    Label("Needs your answer", systemImage: "bubble.left.and.bubble.right")
                        .font(.headline)
                    ExpiryCountdown(expiresAt: question.expiresAt)
                }
                controls
            } else {
                resolvedCard
            }
            if let message {
                Label(message, systemImage: "exclamationmark.circle.fill")
                    .font(.footnote)
                    .foregroundStyle(.red)
            }
        }
        .padding(16)
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 20))
        .task(id: question.expiresAt) {
            let wait = question.expiresAt.timeIntervalSinceNow
            if wait > 0 { try? await Task.sleep(for: .seconds(wait)) }
            if !Task.isCancelled { expiredLocally = true }
        }
    }

    @ViewBuilder
    private var controls: some View {
        if question.isApproval {
            VStack(spacing: 10) {
                Button {
                    submit(choice("approve"), confirm: true)
                } label: {
                    Label("Approve", systemImage: "checkmark.circle.fill")
                        .frame(maxWidth: .infinity, minHeight: 24)
                        .opacity(sending == choice("approve") ? 0 : 1)
                }
                .buttonStyle(.borderedProminent)
                .overlay { if sending == choice("approve") { ProgressView().tint(.white) } }

                Button(role: .destructive) {
                    submit(choice("reject"))
                } label: {
                    Label("Reject", systemImage: "xmark.circle.fill")
                        .frame(maxWidth: .infinity, minHeight: 24)
                        .opacity(sending == choice("reject") ? 0 : 1)
                }
                .buttonStyle(.bordered)
                .overlay { if sending == choice("reject") { ProgressView() } }
            }
            .controlSize(.large)
            .disabled(sending != nil)
        } else if let choices = question.choices {
            VStack(spacing: 10) {
                ForEach(choices, id: \.self) { c in
                    Button {
                        submit(c)
                    } label: {
                        HStack {
                            Text(verbatim: c)
                            if sending == c { ProgressView().padding(.leading, 4) }
                        }
                        .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.bordered)
                }
            }
            .controlSize(.large)
            .disabled(sending != nil)
        }
    }

    private var resolvedCard: some View {
        let look = QuestionAppearance(question)
        return HStack(alignment: .top, spacing: 12) {
            Image(systemName: look.icon)
                .font(.title2)
                .foregroundStyle(look.color)
            VStack(alignment: .leading, spacing: 4) {
                Text(look.headline).font(.headline)
                if let answer = question.answer, !question.isApproval {
                    Text(answer).textSelection(.enabled)
                }
            }
            Spacer(minLength: 0)
        }
        .padding()
        .background(look.color.opacity(0.1), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
    }

    private func queuedNotice(_ reply: PendingReply) -> some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: "clock.arrow.circlepath")
                .font(.title3)
                .foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 4) {
                Text("Waiting to send").font(.headline)
                Text(reply.answer).textSelection(.enabled)
                Text("Your answer is saved and will be sent when the connection is back.")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
        }
        .padding()
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(.secondarySystemFill), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
    }

    /// The question's own spelling of a choice, e.g. "Approve".
    private func choice(_ name: String) -> String {
        question.choices?.first { $0.lowercased() == name } ?? name
    }

    private func submit(_ answer: String, confirm: Bool = false) {
        guard !answer.isEmpty, sending == nil else { return }
        sending = answer
        message = nil
        Task {
            if confirm, !(await DeviceAuth.confirm(reason: String(localized: "Confirm the approval"))) {
                sending = nil
                return
            }
            let result = await model.answer(question.id, with: answer)
            sending = nil
            switch result {
            case .sent, .queued:
                break
            case .refused(let reason):
                message = reason
            }
        }
    }
}

/// A keyboard-aware input bar. It stays outside the scrolling message so
/// large text and multi-line drafts cannot push Send beneath the keyboard.
private struct ReplyComposer: View {
    let question: MeowNotification.Question
    @Environment(AppModel.self) private var model
    @State private var reply = ""
    @State private var sending = false
    @State private var message: String?
    @FocusState private var replyFocused: Bool

    private var canSend: Bool {
        !reply.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && !sending
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            if replyFocused {
                HStack {
                    Text("Reply").font(.subheadline.weight(.semibold)).foregroundStyle(.secondary)
                    Spacer()
                    Button("Done") { replyFocused = false }
                        .frame(minWidth: 44, minHeight: 44)
                        .contentShape(Rectangle())
                }
            }
            if let message {
                Label(message, systemImage: "exclamationmark.circle.fill")
                    .font(.footnote)
                    .foregroundStyle(.red)
            }
            HStack(alignment: .bottom, spacing: 10) {
                TextField("Reply to the agent", text: $reply, axis: .vertical)
                    .lineLimit(1...3)
                    .focused($replyFocused)
                    .disabled(sending)
                    .accessibilityIdentifier("reply.field")
                    .padding(.horizontal, 14)
                    .padding(.vertical, 12)
                    .frame(minHeight: 48)
                    .background(Color(.secondarySystemFill), in: RoundedRectangle(cornerRadius: 18))
                Button(action: submit) {
                    ZStack {
                        if sending {
                            ProgressView()
                        } else {
                            Image(systemName: "arrow.up.circle.fill")
                                .font(.system(size: 36))
                        }
                    }
                    .frame(width: 48, height: 48)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .foregroundStyle(canSend ? Color.accentColor : Color.secondary)
                .disabled(!canSend)
                .accessibilityLabel("Send")
                .accessibilityIdentifier("reply.send")
            }
        }
        .frame(maxWidth: 640)
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
        .frame(maxWidth: .infinity)
        .background(Color(.systemBackground))
        .overlay(alignment: .top) { Divider() }
    }

    private func submit() {
        guard canSend else { return }
        let answer = reply.trimmingCharacters(in: .whitespacesAndNewlines)
        sending = true
        message = nil
        Task {
            let result = await model.answer(question.id, with: answer)
            sending = false
            switch result {
            case .sent, .queued:
                reply = ""
                replyFocused = false
            case .refused(let reason):
                message = reason
            }
        }
    }
}

private struct DetailsSection: View {
    let notification: MeowNotification

    var body: some View {
        let n = notification
        DisclosureGroup("Details") {
            VStack(spacing: 10) {
                LabeledContent("Priority") { Text(priority) }
                LabeledContent("Push") { Text(pushStatus) }
                if !(n.pushError ?? "").isEmpty {
                    Text(n.pushError ?? "")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                LabeledContent("Delivered") { timestamp(n.deliveredAt) }
                LabeledContent("Read") { timestamp(n.readAt) }
                LabeledContent("ID") { mono(n.id) }
                if let q = n.question {
                    LabeledContent("Question ID") { mono(q.id) }
                }
            }
            .font(.subheadline)
            .padding(.top, 8)
        }
        .tint(.secondary)
    }

    private var priority: LocalizedStringKey {
        switch notification.priority {
        case "low": "Low"
        case "high": "High"
        default: "Normal"
        }
    }

    private var pushStatus: LocalizedStringKey {
        switch notification.pushStatus {
        case "accepted": "Sent to Apple"
        case "failed": "Failed"
        case "skipped": "Not pushed"
        default: "Queued"
        }
    }

    @ViewBuilder
    private func timestamp(_ date: Date?) -> some View {
        if let date {
            Text(date.formatted(date: .abbreviated, time: .standard))
        } else {
            Text(verbatim: "—")
        }
    }

    private func mono(_ s: String) -> some View {
        Text(verbatim: s)
            .font(.footnote.monospaced())
            .textSelection(.enabled)
    }
}
