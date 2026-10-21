import SwiftUI

struct InboxView: View {
    @Environment(AppModel.self) private var model
    @State private var now = Date.now
    @State private var isSearching = false
    @State private var deletion: DeletionConfirmation?
    @State private var confirmsDeletion = false
    @FocusState private var searchFocused: Bool

    var body: some View {
        @Bindable var model = model
        NavigationStack(path: $model.inboxPath) {
            List {
                if let error = model.inboxStorageError ?? model.loadError, !model.notifications.isEmpty {
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
            .safeAreaInset(edge: .top, spacing: 0) {
                if isSearching { searchField }
            }
            .navigationTitle("Inbox")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .principal) {
                    InboxMenu()
                }
                ToolbarItemGroup(placement: .topBarTrailing) {
                    deletionMenu
                    if isSearching {
                        Button("Cancel") {
                            searchFocused = false
                            isSearching = false
                            model.search = ""
                        }
                        .accessibilityIdentifier("inbox.cancelSearch")
                    } else {
                        Button { isSearching = true } label: {
                            Label("Search notifications", systemImage: "magnifyingglass")
                        }
                        .accessibilityIdentifier("inbox.search")
                    }
                }
            }
            .alert("Delete messages?", isPresented: $confirmsDeletion, presenting: deletion) { selection in
                Button("Delete", role: .destructive) {
                    Task { await model.deleteNotifications(in: selection.interval) }
                }
                Button("Cancel", role: .cancel) {}
            } message: { selection in
                Text("\(selection.range.title): All messages in this time range will be permanently deleted from this server and this iPhone, including messages hidden by search or filters.")
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

    private struct DeletionConfirmation {
        let range: InboxDeleteRange
        let interval: InboxDeletionInterval
    }

    private var deletionMenu: some View {
        Menu {
            Section("Delete messages from") {
                ForEach(InboxDeleteRange.recent) { range in
                    Button(range.title) { confirmDeletion(range) }
                }
            }
            Menu("Older messages") {
                ForEach(InboxDeleteRange.older) { range in
                    Button(range.title) { confirmDeletion(range) }
                }
            }
        } label: {
            Label("Delete messages", systemImage: "trash")
        }
        .disabled(model.isDeletingNotifications || model.savedNotifications.isEmpty)
        .accessibilityIdentifier("inbox.delete")
    }

    private func confirmDeletion(_ range: InboxDeleteRange) {
        deletion = DeletionConfirmation(range: range, interval: range.interval())
        confirmsDeletion = true
    }

    private var searchField: some View {
        HStack(spacing: 8) {
            Image(systemName: "magnifyingglass")
                .foregroundStyle(.secondary)
                .accessibilityHidden(true)
            TextField("Search notifications", text: Bindable(model).search)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .submitLabel(.search)
                .focused($searchFocused)
                .accessibilityIdentifier("inbox.searchField")
                .onSubmit { searchFocused = false }
        }
        .padding(.horizontal, 12)
        .frame(minHeight: 44)
        .background(Color(.secondarySystemFill), in: RoundedRectangle(cornerRadius: 12))
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
        .background(.bar)
        .task { searchFocused = true }
    }

    @ViewBuilder
    private func inboxRows(now: Date) -> some View {
        rows(model.notifications, now: now)
    }

    private func rows(_ notifications: [MeowNotification], now: Date) -> some View {
        ForEach(notifications) { notification in
            NavigationLink(value: notification.id) {
                NotificationRow(notification: notification, now: now)
            }
            .accessibilityIdentifier("notification.\(notification.id)")
            .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                Button(role: .destructive) {
                    Task { await model.deleteNotification(notification.id) }
                } label: {
                    Label("Delete", systemImage: "trash")
                }
                .disabled(model.isDeletingNotifications)
            }
        }
    }

    @ViewBuilder
    private var emptyState: some View {
        if model.notifications.isEmpty || (model.filter == .pending && InboxPresentation(model.notifications, now: now).pending.isEmpty) {
            if model.isLoading || !model.hasLoaded {
                ProgressView("Syncing messages…")
            } else if let error = model.inboxStorageError ?? model.loadError {
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
                        text("No notifications need attention", "Continue in the agent on your computer.")
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

private struct InboxMenu: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Menu {
            Section("Filter notifications") {
                ForEach(AppModel.Filter.visibleCases) { filter in
                    Button { model.filter = filter } label: {
                        if model.filter == filter {
                            Label { filterTitle(filter) } icon: { Image(systemName: "checkmark") }
                        } else {
                            filterTitle(filter)
                        }
                    }
                    .accessibilityIdentifier("filter.\(filter.rawValue)")
                }
            }
            Section {
                Button("Mark all as read") { Task { await model.markAllRead() } }
                    .disabled(model.counts.unread == 0)
            }
        } label: {
            HStack(spacing: 6) {
                Text("Inbox").font(.headline)
                if model.filter != .all {
                    Text(model.filter.title)
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
                Image(systemName: "chevron.down")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
            }
            .lineLimit(1)
            .frame(minWidth: 44, minHeight: 44)
            .foregroundStyle(Color.primary)
        }
        .accessibilityIdentifier("inbox.filterMenu")
        .accessibilityLabel(Text("Inbox") + Text(verbatim: ", ") + Text("Filter notifications"))
        .accessibilityValue(Text(model.filter.title) + Text(verbatim: " \(count(model.filter).formatted())"))
    }

    private func count(_ filter: AppModel.Filter) -> Int {
        switch filter {
        case .all: 0
        case .unread: model.counts.unread
        case .pending: model.counts.pending
        }
    }

    private func filterTitle(_ filter: AppModel.Filter) -> Text {
        let value = count(filter)
        return value > 0 ? Text(filter.title) + Text(verbatim: " \(value.formatted())") : Text(filter.title)
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
                if let line = NotificationBody(n.body).listPreview {
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
        .accessibilityValue(needsAnswer ? Text("Needs attention") : (n.isUnread ? Text("Unread") : Text(verbatim: "")))
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

}
