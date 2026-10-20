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
                        HStack(spacing: 12) {
                            SourceAvatar(source: n.source, size: 44)
                            VStack(alignment: .leading, spacing: 2) {
                                Text(n.source.isEmpty ? "Meow" : n.source).font(.headline)
                                Text(n.createdAt.formatted(date: .abbreviated, time: .shortened))
                                    .font(.subheadline).foregroundStyle(.secondary)
                            }
                        }
                        VStack(alignment: .leading, spacing: 12) {
                            Text(n.title).font(.title2.bold()).textSelection(.enabled)
                            if !n.body.isEmpty { Text(n.body).textSelection(.enabled) }
                        }
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
