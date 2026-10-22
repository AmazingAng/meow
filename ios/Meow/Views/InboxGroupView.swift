import SwiftUI

struct InboxGroupRow: View {
    var group: InboxGroup
    var isSearching = false
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            if !typeSize.isAccessibilitySize {
                let rgb = OriginColor(source: group.colorSource, context: group.colorContext)
                RoundedRectangle(cornerRadius: 9)
                    .fill(Color(red: rgb.red, green: rgb.green, blue: rgb.blue))
                    .overlay(Image(systemName: group.mergeID == nil ? group.mode.symbol : "square.stack.3d.up")
                        .foregroundStyle(.white).font(.system(size: 16, weight: .semibold)))
                    .frame(width: 32, height: 32).accessibilityHidden(true)
            }
            VStack(alignment: .leading, spacing: 6) {
                HStack(alignment: .firstTextBaseline) {
                    Text(verbatim: group.title).font(.headline)
                        .lineLimit(typeSize.isAccessibilitySize ? nil : 2)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    if group.hasAttention {
                        Image(systemName: "exclamationmark.circle.fill").foregroundStyle(.orange)
                            .accessibilityLabel("Unread attention or failure")
                    }
                    if group.unreadCount > 0 && !isSearching {
                        Text(group.unreadCount.formatted()).font(.caption.bold()).monospacedDigit()
                            .padding(.horizontal, 7).padding(.vertical, 3)
                            .background(Color.accentColor.opacity(0.12), in: Capsule())
                    }
                }
                if !group.subtitle.isEmpty {
                    Text(verbatim: group.subtitle).font(.caption).foregroundStyle(.secondary)
                        .lineLimit(typeSize.isAccessibilitySize ? nil : 1)
                }
                if let latest = group.latest {
                    if let preview = NotificationBody(latest.body).listPreview {
                        HStack(alignment: .top, spacing: 5) {
                            Image(systemName: latest.event?.symbol ?? "bubble.left").accessibilityHidden(true)
                            Text(preview).lineLimit(2)
                        }
                        .font(.subheadline).foregroundStyle(.secondary)
                    }
                    RelativeTime(date: latest.createdAt).font(.caption).foregroundStyle(.secondary)
                }
                if isSearching {
                    Text("\(group.unreadCount) matching unread messages").font(.caption).foregroundStyle(.secondary)
                }
            }
        }
        .padding(.vertical, 8)
        .accessibilityElement(children: .combine)
        .accessibilityValue(Text("\(group.unreadCount) matching unread messages"))
        .accessibilityHint(Text(group.mode.title))
    }
}

struct InboxGroupView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    var id: String
    @State private var fallback: InboxGroup?
    init(id: String, initial: InboxGroup?) { self.id = id; _fallback = State(initialValue: initial) }
    @State private var displayLimit = 50
    @State private var isSearching = false
    @State private var showsMembers = false
    @FocusState private var searchFocused: Bool

    private var current: InboxGroup? { model.inboxGroup(id) }
    private var presentation: InboxGroup? { current ?? fallback }
    private var matches: [MeowNotification] { current?.matches ?? [] }
    private var failedIDs: Set<String> {
        model.failedGroupReadIDs.intersection(Set(current?.matches.map(\.id) ?? []))
    }

    var body: some View {
        List {
            if let group = presentation, !group.subtitle.isEmpty {
                Text(verbatim: group.subtitle).font(.subheadline).foregroundStyle(.secondary)
                    .listRowSeparator(.hidden)
            }
            ForEach(Array(matches.prefix(displayLimit))) { n in
                NavigationLink(value: n.id) { NotificationRow(notification: n, now: .now) }
                    .accessibilityIdentifier("notification.\(n.id)")
                    .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                        Button(role: .destructive) { Task { await model.deleteNotification(n.id) } } label: {
                            Label("Delete", systemImage: "trash")
                        }.disabled(model.isDeletingNotifications)
                    }
            }
            if matches.count > displayLimit {
                ProgressView().frame(maxWidth: .infinity)
                    .task(id: displayLimit) { displayLimit += 50 }
            }
        }
        .listStyle(.plain)
        .navigationTitle(presentation?.title ?? String(localized: "Group"))
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await model.refresh() }
        .scrollDismissesKeyboard(.interactively)
        .overlay {
            if matches.isEmpty {
                if model.isGroupingInbox { ProgressView("Syncing messages…") }
                else {
                    ContentUnavailableView {
                        Label("No matching messages", systemImage: "tray")
                    } description: {
                        Text("This group has no messages matching the current filters.")
                    } actions: {
                        if !model.search.isEmpty || model.filter != .all {
                            Button("Clear search and filters") { model.search = ""; model.filter = .all }
                        }
                    }
                }
            }
        }
        .safeAreaInset(edge: .top, spacing: 0) {
            if isSearching || !model.search.isEmpty {
                HStack {
                    TextField("Search notifications", text: Bindable(model).search)
                        .textInputAutocapitalization(.never).autocorrectionDisabled().focused($searchFocused)
                        .accessibilityIdentifier("group.searchField")
                    Button("Cancel") { model.search = ""; isSearching = false; searchFocused = false }
                }
                .padding(12).background(.bar)
            }
        }
        .toolbar {
            ToolbarItemGroup(placement: .topBarTrailing) {
                if model.isMarkingGroupRead || model.isGroupingInbox { ProgressView() }
                Button { isSearching.toggle(); searchFocused = isSearching } label: {
                    Label("Search notifications", systemImage: "magnifyingglass")
                }
                .accessibilityIdentifier("group.search")
                Menu {
                    Section("Filter notifications") {
                        ForEach(AppModel.Filter.visibleCases) { filter in
                            Button { model.filter = filter } label: {
                                if model.filter == filter { Label { Text(filter.title) } icon: { Image(systemName: "checkmark") } }
                                else { Text(filter.title) }
                            }
                        }
                    }
                    Button("Mark matching messages as read") {
                        let ids = Set(matches.filter(\.isUnread).map(\.id))
                        Task { await model.markMatchingRead(ids) }
                    }
                    .disabled(model.isGroupingInbox || model.isMarkingGroupRead || model.isMarkingAllRead || !matches.contains(where: \.isUnread))
                    .accessibilityIdentifier("group.markRead")
                    if !failedIDs.isEmpty {
                        Button("Retry failed reads") {
                            let ids = failedIDs
                            Task { await model.markMatchingRead(ids) }
                        }.disabled(model.isMarkingGroupRead || model.isMarkingAllRead || model.isGroupingInbox)
                    }
                    if presentation?.mergeID != nil {
                        Button("Group members") { showsMembers = true }
                            .accessibilityIdentifier("group.members")
                    }
                } label: { Label("Group actions", systemImage: "ellipsis.circle") }
                .accessibilityIdentifier("group.actions")
            }
        }
        .onChange(of: model.search) { displayLimit = 50 }
        .onChange(of: model.filter) { displayLimit = 50 }
        .sheet(isPresented: $showsMembers) {
            NavigationStack {
                List {
                    if let group = presentation {
                        Section("Group members") {
                            ForEach(group.visibleMembers) { member in
                                VStack(alignment: .leading, spacing: 4) {
                                    Text(verbatim: member.title)
                                    if !member.subtitle.isEmpty {
                                        Text(verbatim: member.subtitle).font(.caption).foregroundStyle(.secondary)
                                    }
                                }
                            }
                        }
                        Section {
                            Button("Unmerge group") {
                                model.unmergeInboxGroup(group)
                                showsMembers = false
                                dismiss()
                            }
                            .accessibilityIdentifier("group.unmerge")
                        } footer: { Text("Only the local grouping changes. Messages and read states are kept.") }
                    }
                }
                .navigationTitle("Group members")
                .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { showsMembers = false } } }
            }
        }
    }
}
