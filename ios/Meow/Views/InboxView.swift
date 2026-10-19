import SwiftUI

struct InboxView: View {
    @Environment(AppModel.self) private var model
    @State private var now = Date.now

    var body: some View {
        @Bindable var model = model
        NavigationStack(path: $model.inboxPath) {
            VStack(spacing: 0) {
                FilterBar(pendingCount: pendingCount)
                List {
                    if let error = model.loadError, !model.notifications.isEmpty {
                        Label(error, systemImage: "exclamationmark.triangle.fill")
                            .font(.footnote)
                            .foregroundStyle(.orange)
                    }
                    inboxRows(now: now)
                    if model.nextCursor != nil {
                        ProgressView()
                            .frame(maxWidth: .infinity)
                            .listRowSeparator(.hidden)
                            .task(id: model.nextCursor) { await model.loadMore() }
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
            .task {
                if !model.hasLoaded { await model.refresh() }
            }
            .task(id: model.notifications) {
                now = .now
                // Only update the list when a pending question actually expires.
                while let expiry = model.notifications.compactMap({ notification -> Date? in
                    guard let question = notification.question, question.isOpen() else { return nil }
                    return question.expiresAt
                }).min() {
                    do { try await Task.sleep(for: .seconds(max(0, expiry.timeIntervalSinceNow) + 0.1)) }
                    catch { return }
                    now = .now
                }
            }
        }
    }

    @ViewBuilder
    private func inboxRows(now: Date) -> some View {
        let groups = InboxPresentation(model.notifications, now: now)
        if model.search.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, model.filter == .all {
            if pendingCount > 0 {
                Section {
                    rows(groups.pending, now: now)
                    if groups.pending.isEmpty {
                        Button("View all pending questions") { model.filter = .pending }
                    }
                } header: {
                    Button { model.filter = .pending } label: {
                        VStack(alignment: .leading, spacing: 4) {
                            Label("Needs your answer: \(pendingCount)",
                                  systemImage: "bubble.left.and.bubble.right.fill")
                                .font(.headline)
                            Text("Soonest expiry first").font(.caption).foregroundStyle(.secondary)
                        }
                        .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(Color.accentColor)
                    .accessibilityIdentifier("inbox.pendingSummary")
                }
            }

            if !groups.activity.isEmpty {
                Section("Other notifications") { rows(groups.activity, now: now) }
            }
        } else if model.filter == .pending {
            rows(groups.pending, now: now)
        } else {
            rows(model.notifications, now: now)
        }
    }

    private var pendingCount: Int {
        // These views now hold the complete pending set, so local answers and
        // expiry can update the count without waiting for another network poll.
        if model.hasLoaded, model.loadError == nil,
           model.search.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
           model.filter == .all || model.filter == .pending {
            return InboxPresentation(model.notifications, now: now).pending.count
        }
        return max(model.counts.pending, InboxPresentation(model.notifications, now: now).pending.count)
    }

    private func rows(_ notifications: [MeowNotification], now: Date) -> some View {
        ForEach(notifications) { notification in
            NavigationLink(value: notification.id) {
                NotificationRow(notification: notification, now: now)
            }
            .accessibilityIdentifier("notification.\(notification.id)")
        }
    }

    @ViewBuilder
    private var emptyState: some View {
        if model.notifications.isEmpty || (model.filter == .pending && InboxPresentation(model.notifications, now: now).pending.isEmpty) {
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
    var pendingCount: Int
    @Environment(AppModel.self) private var model
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        Group {
            if typeSize.isAccessibilitySize {
                Menu {
                    ForEach(AppModel.Filter.allCases) { filter in
                        Button { model.filter = filter } label: {
                            if model.filter == filter {
                                Label(filter.title, systemImage: "checkmark")
                            } else {
                                Text(filter.title)
                            }
                        }
                        .accessibilityIdentifier("filter.\(filter.rawValue)")
                    }
                } label: {
                    HStack {
                        Text(model.filter.title)
                        Spacer()
                        Image(systemName: "line.3.horizontal.decrease.circle")
                    }
                    .font(.headline)
                    .frame(minHeight: 44)
                    .contentShape(Rectangle())
                }
                .accessibilityIdentifier("inbox.filterMenu")
                .accessibilityLabel(Text("Filter notifications"))
                .accessibilityValue(Text(model.filter.title))
                .padding(.horizontal, 16)
            } else {
                ScrollView(.horizontal) {
                    HStack(spacing: 8) {
                        ForEach(AppModel.Filter.allCases) { filter in chip(filter) }
                    }
                    .padding(.horizontal, 16)
                }
                .scrollIndicators(.hidden)
            }
        }
        .padding(.vertical, 8)
        .accessibilityElement(children: .contain)
    }

    private func chip(_ filter: AppModel.Filter) -> some View {
        let selected = model.filter == filter
        let count = switch filter {
        case .all: 0
        case .unread: model.counts.unread
        case .pending: pendingCount
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
    var now: Date
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        row(now: now)
    }

    private func row(now: Date) -> some View {
        let n = notification
        let needsAnswer = n.question?.isOpen(at: now) == true
        return HStack(alignment: .top, spacing: 12) {
            if !typeSize.isAccessibilitySize {
                SourceAvatar(source: n.source, size: 32)
            }
            VStack(alignment: .leading, spacing: 6) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Text(n.title)
                        .font(.headline)
                        .fontWeight(needsAnswer || n.isUnread ? .semibold : .regular)
                        .lineLimit(typeSize.isAccessibilitySize ? nil : 3)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    if n.isUnread {
                        Circle().fill(Color.accentColor).frame(width: 8, height: 8)
                            .accessibilityHidden(true)
                    }
                }
                if typeSize.isAccessibilitySize {
                    VStack(alignment: .leading, spacing: 4) { metadata }
                } else {
                    HStack(alignment: .firstTextBaseline) { metadata }
                }
                if let line = firstLine(of: n.body) {
                    Text(line)
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .lineLimit(typeSize.isAccessibilitySize ? 3 : 1)
                }
                if let question = n.question {
                    QuestionStatusTag(question: question)
                }
            }
        }
        .padding(.vertical, 8)
        .accessibilityElement(children: .combine)
        .accessibilityValue(needsAnswer ? Text("Needs your answer") : (n.isUnread ? Text("Unread") : Text(verbatim: "")))
    }

    @ViewBuilder
    private var metadata: some View {
        Text(notification.source.isEmpty ? "Meow" : notification.source)
            .font(.subheadline)
            .foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
        if !typeSize.isAccessibilitySize { Spacer(minLength: 8) }
        RelativeTime(date: notification.createdAt)
            .font(.caption)
            .foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
    }

    private func firstLine(of body: String) -> String? {
        let line = body.split(whereSeparator: \.isNewline).first.map(String.init)?
            .trimmingCharacters(in: .whitespaces)
        return line?.isEmpty == false ? line : nil
    }
}
