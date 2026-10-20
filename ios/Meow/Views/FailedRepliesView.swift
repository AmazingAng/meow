import SwiftUI

struct FailedRepliesView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        List {
            if !model.queuedReplies.isEmpty {
                Section {
                    ForEach(model.queuedReplies.values.sorted { $0.createdAt > $1.createdAt }, id: \.idempotencyKey) { reply in
                        VStack(alignment: .leading, spacing: 8) {
                            Text(reply.answer).textSelection(.enabled)
                            CopyButton(title: "Copy answer", text: reply.answer)
                        }
                    }
                } header: {
                    Text("Unsent records")
                } footer: {
                    Text("Meow V1 does not send saved replies. You can copy this text and respond on your computer.")
                }
            }
            Section {
                ForEach(model.failedReplies.sorted { $0.createdAt > $1.createdAt }, id: \.idempotencyKey) { reply in
                    VStack(alignment: .leading, spacing: 10) {
                        Label("Your answer wasn't accepted", systemImage: "exclamationmark.circle")
                            .font(.headline)
                            .foregroundStyle(.red)
                        Text(reply.answer).textSelection(.enabled)
                        if let failure = reply.failure {
                            Text(AppModel.describeRefusal(failure.error))
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                        }
                        Text(reply.createdAt.formatted(date: .abbreviated, time: .shortened))
                            .font(.caption)
                            .foregroundStyle(.secondary)
                        CopyButton(title: "Copy answer", text: reply.answer)
                    }
                    .padding(.vertical, 4)
                    .swipeActions {
                        Button("Delete", role: .destructive) {
                            Task { await model.dismissFailedReply(reply) }
                        }
                    }
                }
            } footer: {
                Text("These answers were refused by the server and won't be sent again. You can copy an answer or delete its saved record.")
            }
        }
        .navigationTitle("Saved reply records")
        .overlay {
            if model.failedReplies.isEmpty && model.queuedReplies.isEmpty {
                ContentUnavailableView("No failed replies", systemImage: "checkmark.bubble")
            }
        }
    }
}
