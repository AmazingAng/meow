import SwiftUI

struct InboxView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        NavigationStack(path: $model.inboxPath) {
            VStack(spacing: 0) {
                FilterBar()
                List {
                    if let error = model.loadError, !model.notifications.isEmpty {
                        Label(error, systemImage: "exclamationmark.triangle.fill")
                            .font(.footnote)
                            .foregroundStyle(.orange)
                    }
                    ForEach(model.notifications) { n in
                        NavigationLink(value: n.id) {
                            NotificationRow(notification: n)
                        }
                    }
                    if model.nextCursor != nil {
                        ProgressView()
                            .frame(maxWidth: .infinity)
                            .listRowSeparator(.hidden)
                            .task { await model.loadMore() }
                    }
                }
                .listStyle(.plain)
                .overlay { emptyState }
                .scrollDismissesKeyboard(.interactively)
                .refreshable { await model.refresh() }
            }
            .navigationTitle("Inbox")
            .searchable(text: $model.search, prompt: Text("Search notifications"))
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        Task { await model.markAllRead() }
                    } label: {
                        Label("Mark all as read", systemImage: "envelope.open")
                    }
                    .disabled(model.counts.unread == 0)
                }
            }
            .navigationDestination(for: String.self) { id in
                DetailView(id: id)
            }
        }
    }

    @ViewBuilder
    private var emptyState: some View {
        if model.notifications.isEmpty {
            if !model.hasLoaded {
                ProgressView()
            } else if let error = model.loadError {
                ContentUnavailableView {
                    Label("Can't load notifications", systemImage: "wifi.exclamationmark")
                } description: {
                    Text(error)
                } actions: {
                    Button("Try again") { Task { await model.refresh() } }
                }
            } else if !model.search.trimmingCharacters(in: .whitespaces).isEmpty {
                ContentUnavailableView.search(text: model.search)
            } else {
                MascotEmptyState(filter: model.filter)
            }
        }
    }
}

private struct MascotEmptyState: View {
    var filter: AppModel.Filter

    var body: some View {
        GeometryReader { geometry in
            ScrollView {
                VStack(spacing: 16) {
                    Image("Mascot")
                        .resizable()
                        .scaledToFit()
                        .frame(width: 120)
                        .accessibilityHidden(true)
                    switch filter {
                    case .all:
                        text("No notifications yet", "Run meow \"Hello\" in your terminal, or send a test from Settings.")
                    case .unread:
                        text("All caught up", "You've read everything.")
                    case .pending:
                        text("Nothing waiting on you", "Questions from your agents show up here until you answer them.")
                    }
                }
                .frame(maxWidth: 480)
                .padding(24)
                .frame(maxWidth: .infinity, minHeight: geometry.size.height)
            }
            .scrollIndicators(.hidden)
        }
    }

    private func text(_ title: LocalizedStringKey, _ detail: LocalizedStringKey) -> some View {
        VStack(spacing: 6) {
            Text(title).font(.title3.weight(.semibold))
            Text(detail)
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
        }
    }
}

private struct FilterBar: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        ScrollView(.horizontal) {
            HStack(spacing: 8) {
                ForEach(AppModel.Filter.allCases) { filter in
                    chip(filter)
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 8)
        }
        .scrollIndicators(.hidden)
        .accessibilityElement(children: .contain)
    }

    private func chip(_ filter: AppModel.Filter) -> some View {
        let selected = model.filter == filter
        let count = switch filter {
        case .all: 0
        case .unread: model.counts.unread
        case .pending: model.counts.pending
        }
        return Button {
            model.filter = filter
        } label: {
            HStack(spacing: 6) {
                if selected {
                    Image(systemName: "checkmark")
                        .font(.caption.weight(.bold))
                        .foregroundStyle(Color.accentColor)
                }
                Text(filter.title)
                if count > 0 {
                    Text(count, format: .number)
                        .font(.caption.weight(.bold))
                        .padding(.horizontal, 6)
                        .padding(.vertical, 1)
                        .background(selected ? Color.white.opacity(0.25) : Color.accentColor.opacity(0.15), in: Capsule())
                }
            }
            .font(.subheadline.weight(.medium))
            .padding(.horizontal, 14)
            .frame(minHeight: 44)
            .fixedSize(horizontal: true, vertical: false)
            .foregroundStyle(Color.primary)
            .background(selected ? Color.accentColor.opacity(0.16) : Color(.secondarySystemFill), in: Capsule())
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("filter.\(filter.rawValue)")
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}

struct NotificationRow: View {
    var notification: MeowNotification

    var body: some View {
        let n = notification
        HStack(alignment: .top, spacing: 12) {
            SourceAvatar(source: n.source)
            VStack(alignment: .leading, spacing: 3) {
                HStack(alignment: .firstTextBaseline) {
                    Text(n.source.isEmpty ? n.title : n.source)
                        .font(.headline)
                        .lineLimit(1)
                    Spacer(minLength: 8)
                    RelativeTime(date: n.createdAt)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                HStack(alignment: .top, spacing: 8) {
                    VStack(alignment: .leading, spacing: 3) {
                        if !n.source.isEmpty {
                            Text(n.title)
                                .font(.subheadline)
                                .fontWeight(n.isUnread ? .semibold : .regular)
                                .lineLimit(2)
                        }
                        if let line = firstLine(of: n.body) {
                            Text(line)
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                        }
                        if let q = n.question {
                            QuestionStatusTag(question: q).padding(.top, 2)
                        }
                    }
                    Spacer(minLength: 0)
                    statusDot.padding(.top, 5)
                }
            }
        }
        .padding(.vertical, 8)
        .accessibilityElement(children: .combine)
        .accessibilityValue(accessibilityStatus)
    }

    /// Red: waiting for your answer. Blue: unread.
    @ViewBuilder
    private var statusDot: some View {
        if notification.question?.isOpen() == true {
            Circle().fill(.red).frame(width: 10, height: 10)
        } else if notification.isUnread {
            Circle().fill(Color.accentColor).frame(width: 10, height: 10)
        } else {
            Color.clear.frame(width: 10, height: 10)
        }
    }

    private var accessibilityStatus: Text {
        if notification.question?.isOpen() == true { return Text("Needs your answer") }
        if notification.isUnread { return Text("Unread") }
        return Text(verbatim: "")
    }

    private func firstLine(of body: String) -> String? {
        let line = body.split(whereSeparator: \.isNewline).first.map(String.init)?
            .trimmingCharacters(in: .whitespaces)
        return line?.isEmpty == false ? line : nil
    }
}
