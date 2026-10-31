import Foundation

struct InboxPreview: Sendable {
    let body: String
    let text: AttributedString?
}

/// Shared by message and group rows. Parsing runs on this actor's executor;
/// both entry count and retained source bytes are bounded, including nil previews.
actor InboxPreviewCache {
    static let shared = InboxPreviewCache()
    private struct Entry {
        let preview: InboxPreview
        let bytes: Int
        var used: UInt64
    }
    private let capacity: Int
    private let byteLimit: Int
    private let parser: @Sendable (String) -> AttributedString?
    private var entries: [String: Entry] = [:]
    private var bytes = 0
    private var tick: UInt64 = 0

    init(capacity: Int = 128, byteLimit: Int = 512 * 1024,
         parser: @escaping @Sendable (String) -> AttributedString? = { NotificationBody($0).listPreview }) {
        self.capacity = max(0, capacity)
        self.byteLimit = max(0, byteLimit)
        self.parser = parser
    }

    func preview(_ body: String) -> InboxPreview {
        guard !Task.isCancelled else { return InboxPreview(body: body, text: nil) }
        tick &+= 1
        if var entry = entries[body] {
            entry.used = tick
            entries[body] = entry
            return entry.preview
        }
        let value = InboxPreview(body: body, text: parser(body))
        let cost = body.utf8.count
        guard !Task.isCancelled, capacity > 0, cost <= byteLimit else { return value }
        while entries.count >= capacity || bytes > byteLimit - cost {
            guard let oldest = entries.min(by: { $0.value.used < $1.value.used }) else { break }
            bytes -= oldest.value.bytes
            entries[oldest.key] = nil
        }
        entries[body] = Entry(preview: value, bytes: cost, used: tick)
        bytes += cost
        return value
    }
}
