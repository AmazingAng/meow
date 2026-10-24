import SwiftUI

struct DetailView: View {
    let id: String
    @Environment(AppModel.self) private var model
    @State private var loadFailed = false

    var body: some View {
        Group {
            if let n = model.notification(id: id) {
                ScrollView {
                    VStack(alignment: .leading, spacing: 24) {
                        NotificationDetailHeader(notification: n)
                        if !n.body.isEmpty { NotificationBodyView(n.body) }
                        if let question = n.question {
                            QuestionContext(question: question)
                        } else if n.type == .alert {
                            Label("Continue in the agent on your computer.", systemImage: "desktopcomputer")
                                .foregroundStyle(.secondary)
                        }
                        Divider()
                        DetailsSection(notification: n)
                    }
                    .frame(maxWidth: 640, alignment: .leading)
                    .padding(20)
                    .frame(maxWidth: .infinity, alignment: .center)
                }
                .background(Color(.systemGroupedBackground))
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
            await model.markRead(id)
        }
    }
}

private struct NotificationDetailHeader: View {
    let notification: MeowNotification

    var body: some View {
        let n = notification
        VStack(alignment: .leading, spacing: 6) {
            Text(verbatim: "\(emoji) \(n.compactHeadline)")
                .font(.title2.bold())
                .fixedSize(horizontal: false, vertical: true)
                .textSelection(.enabled)
                .accessibilityAddTraits(.isHeader)
                .accessibilityValue(categoryDescription)
                .accessibilityIdentifier("notification.heading")
            Text(verbatim: n.compactMetadata)
                .font(.subheadline).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .textSelection(.enabled)
                .accessibilityIdentifier("notification.metadata")
            Text(n.createdAt.formatted(date: .abbreviated, time: .shortened))
                .font(.caption).foregroundStyle(.secondary)
                .accessibilityIdentifier("notification.date")
        }
    }

    private var emoji: String {
        switch notification.inboxCategory {
        case .result: "💬"
        case .attention: "🔔"
        case .failure: "⚠️"
        case nil: notification.type == .alert ? "🔔" : "💬"
        }
    }

    private var categoryDescription: Text {
        switch notification.inboxCategory {
        case .result: Text("New result")
        case .attention: Text("Needs attention")
        case .failure: Text("Run failed")
        case nil: Text("Notification")
        }
    }
}

/// Legacy questions remain readable. V1 has no answer or approval controls.
private struct QuestionContext: View {
    let question: MeowNotification.Question

    var body: some View {
        TimelineView(.periodic(from: .now, by: 60)) { context in
            let appearance = QuestionAppearance(question, now: context.date)
            VStack(alignment: .leading, spacing: 12) {
                Label(appearance.headline, systemImage: appearance.icon)
                    .font(.headline).foregroundStyle(appearance.color)
                if let choices = question.choices, !choices.isEmpty {
                    Text("Options from the agent").font(.subheadline).foregroundStyle(.secondary)
                    ForEach(Array(choices.enumerated()), id: \.offset) { index, choice in
                        Text("\(index + 1). \(choice)")
                            .textSelection(.enabled)
                    }
                }
                if let answer = question.answer, !answer.isEmpty {
                    Text(answer).textSelection(.enabled)
                }
                Text("Meow V1 only delivers notifications. Continue in the agent on your computer.")
                    .font(.subheadline).foregroundStyle(.secondary)
            }
            .padding(16)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 20))
            .accessibilityIdentifier("question.readOnly")
        }
    }
}

private struct DetailsSection: View {
    let notification: MeowNotification

    var body: some View {
        let n = notification
        DisclosureGroup("Details") {
            VStack(spacing: 10) {
                LabeledContent("Title") {
                    Text(verbatim: n.title).textSelection(.enabled)
                }
                .accessibilityIdentifier("notification.originalTitle")
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
                if !n.body.isEmpty {
                    DisclosureGroup("Original text") {
                        Text(verbatim: n.body)
                            .font(.footnote.monospaced())
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.top, 8)
                    }
                    .accessibilityIdentifier("notification.original")
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
