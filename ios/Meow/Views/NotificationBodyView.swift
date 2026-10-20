import SwiftUI

struct NotificationBodyView: View {
    private let content: NotificationBody

    init(_ body: String) {
        content = NotificationBody(body)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            if let json = content.json {
                code(json)
            } else {
                ForEach(content.blocks.indices, id: \.self) { index in
                    let block = content.blocks[index]
                    if let table = block.tableID {
                        if index == 0 || content.blocks[index - 1].tableID != table {
                            tableView(table)
                        }
                    } else {
                        blockView(block)
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .textSelection(.enabled)
        .accessibilityIdentifier("notification.body")
    }

    @ViewBuilder
    private func blockView(_ block: NotificationBody.Block) -> some View {
        if block.isDivider {
            Divider()
        } else if block.isCode {
            code(String(block.text.characters))
        } else {
            HStack(alignment: .top, spacing: 8) {
                if block.isQuote {
                    RoundedRectangle(cornerRadius: 2).fill(.secondary.opacity(0.4)).frame(width: 3)
                }
                if let marker = block.marker {
                    Text(verbatim: marker).foregroundStyle(.secondary)
                }
                Text(block.text)
                    .font(font(for: block.heading))
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .fixedSize(horizontal: false, vertical: true)
            .padding(.leading, CGFloat(max(0, block.listDepth - 1)) * 16)
        }
    }

    private func tableView(_ id: Int) -> some View {
        let cells = content.blocks.filter { $0.tableID == id }
        let rows = cells.reduce(into: [Int]()) { rows, cell in
            if let row = cell.rowID, rows.last != row { rows.append(row) }
        }
        return ScrollView(.horizontal) {
            Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 10) {
                ForEach(rows, id: \.self) { row in
                    GridRow {
                        let columns = cells.filter { $0.rowID == row }
                        ForEach(columns.indices, id: \.self) { column in
                            Text(columns[column].text)
                                .fontWeight(columns[column].isTableHeader ? .semibold : .regular)
                                .frame(maxWidth: 240, alignment: .leading)
                        }
                    }
                    Divider()
                }
            }
            .padding(12)
        }
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 12))
    }

    private func font(for heading: Int?) -> Font {
        switch heading {
        case 1: .title2.bold()
        case 2: .title3.bold()
        case .some: .headline
        case nil: .body
        }
    }

    private func code(_ text: String) -> some View {
        ScrollView(.horizontal) {
            Text(verbatim: text)
                .font(.callout.monospaced())
                .fixedSize(horizontal: true, vertical: false)
                .padding(12)
        }
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 12))
    }
}
