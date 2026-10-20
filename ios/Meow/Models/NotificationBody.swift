import Foundation

/// A presentation of the body; the stored notification and its original text never change.
struct NotificationBody {
    struct Block {
        var text: AttributedString
        let intents: [PresentationIntent.IntentType]
        var marker: String?

        var heading: Int? {
            for intent in intents {
                if case .header(let level) = intent.kind { return level }
            }
            return nil
        }
        var isCode: Bool { intents.contains { if case .codeBlock = $0.kind { return true }; return false } }
        var isQuote: Bool { intents.contains { $0.kind == .blockQuote } }
        var isDivider: Bool { intents.contains { $0.kind == .thematicBreak } }
        var listDepth: Int { intents.filter { $0.kind == .orderedList || $0.kind == .unorderedList }.count }
        var tableID: Int? {
            intents.first { if case .table = $0.kind { return true }; return false }?.identity
        }
        var rowID: Int? {
            intents.first {
                if case .tableRow = $0.kind { return true }
                return $0.kind == .tableHeaderRow
            }?.identity
        }
        var isTableHeader: Bool { intents.contains { $0.kind == .tableHeaderRow } }
    }

    let original: String
    let json: String?
    let blocks: [Block]

    init(_ original: String) {
        self.original = original
        var markdown = original
        let trimmed = original.trimmingCharacters(in: .whitespacesAndNewlines)
        if (trimmed.hasPrefix("{") || trimmed.hasPrefix("[")),
           let value = try? JSONSerialization.jsonObject(with: Data(trimmed.utf8)) {
            // Only unwrap an unambiguous text envelope. Never discard sibling fields.
            if let object = value as? [String: Any], object.count == 1,
               let entry = object.first,
               ["suggestion", "message", "text", "content"].contains(entry.key),
               let text = entry.value as? String,
               !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                markdown = text
            } else {
                json = Self.formatJSON(trimmed)
                blocks = []
                return
            }
        }
        json = nil
        guard let parsed = try? AttributedString(markdown: markdown),
              !parsed.characters.isEmpty else {
            blocks = [Block(text: AttributedString(markdown), intents: [])]
            return
        }
        var result: [Block] = []
        var seenItems = Set<Int>()
        for run in parsed.runs {
            let intents = run.presentationIntent?.components ?? []
            var text = AttributedString(parsed[run.range])
            // Layout is rendered below; retain native inline emphasis/code/link attributes.
            text.presentationIntent = nil
            if let link = text.link,
               !["https", "http", "mailto"].contains(link.scheme?.lowercased() ?? "") {
                text.link = nil
            }
            if let last = result.last, last.intents == intents {
                result[result.count - 1].text.append(text)
            } else {
                var marker: String?
                for (index, intent) in intents.enumerated() {
                    if case .listItem(let ordinal) = intent.kind {
                        if seenItems.insert(intent.identity).inserted {
                            let ordered = intents.dropFirst(index + 1).first?.kind == .orderedList
                            marker = ordered ? "\(ordinal)." : "•"
                        }
                        break
                    }
                }
                result.append(Block(text: text, intents: intents, marker: marker))
            }
        }
        blocks = result
    }

    /// Format only whitespace outside strings. Re-encoding JSON would change number
    /// precision, key order, duplicate keys and escape sequences in agent output.
    private static func formatJSON(_ source: String) -> String {
        var result = ""
        var depth = 0
        var inString = false
        var escaped = false
        var previous: Character?
        func newline() -> String { "\n" + String(repeating: "  ", count: depth) }
        for character in source {
            if inString {
                result.append(character)
                if escaped { escaped = false }
                else if character == "\\" { escaped = true }
                else if character == "\"" { inString = false }
                continue
            }
            if character.isWhitespace { continue }
            if previous == "{" || previous == "[" {
                if character != "}" && character != "]" { result += newline() }
            }
            switch character {
            case "\"": inString = true; result.append(character)
            case "{", "[": result.append(character); depth += 1
            case "}", "]":
                depth -= 1
                if previous != "{" && previous != "[" { result += newline() }
                result.append(character)
            case ",": result += "," + newline()
            case ":": result += ": "
            default: result.append(character)
            }
            previous = character
        }
        return result
    }
}
