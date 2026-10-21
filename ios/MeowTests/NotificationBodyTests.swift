import XCTest
@testable import Meow

final class NotificationBodyTests: XCTestCase {
    func testReportedSuggestionShowsHumanTextAndKeepsOriginal() {
        let raw = #"{"suggestion":"收到了，自动推送正常了"}"#
        let body = NotificationBody(raw)
        XCTAssertNil(body.json)
        XCTAssertEqual(body.original, raw)
        XCTAssertEqual(body.blocks.map { String($0.text.characters) }, ["收到了，自动推送正常了"])
    }

    func testReportedMarkdownRendersStrongEmphasis() {
        let body = NotificationBody("收到，这条回复用于测试桌面版的自动推送。\n\n回复结束后，留意 Meow 是否收到 **「New result」** 通知。")
        XCTAssertEqual(body.blocks.count, 2)
        XCTAssertEqual(String(body.blocks[1].text.characters), "回复结束后，留意 Meow 是否收到 「New result」 通知。")
        XCTAssertTrue(body.blocks[1].text.runs.contains {
            $0.inlinePresentationIntent?.contains(.stronglyEmphasized) == true
        })
    }

    func testReportedTestFlightMessageRendersChineseStrongAndInlineCode() {
        let raw = """
        **0.1.0（14）已归档并验证，尚未上传成功。**

        命令行报 `Failed to Use Accounts`。已用 Xcode 打开归档，请选 build 14，点击 **Distribute App → TestFlight & App Store → Distribute**。完成后告诉我结果，我继续核对 TestFlight 状态。
        """
        let body = NotificationBody(raw)
        XCTAssertEqual(body.original, raw)
        XCTAssertNil(body.json)
        XCTAssertEqual(body.blocks.count, 2)
        XCTAssertEqual(String(body.blocks[0].text.characters), "0.1.0（14）已归档并验证，尚未上传成功。")
        XCTAssertEqual(String(body.blocks[1].text.characters),
            "命令行报 Failed to Use Accounts。已用 Xcode 打开归档，请选 build 14，点击 Distribute App → TestFlight & App Store → Distribute。完成后告诉我结果，我继续核对 TestFlight 状态。")
        let strong = body.blocks.flatMap { block in
            block.text.runs.filter { $0.inlinePresentationIntent?.contains(.stronglyEmphasized) == true }
                .map { String(block.text[$0.range].characters) }
        }
        XCTAssertEqual(strong, ["0.1.0（14）已归档并验证，尚未上传成功。", "Distribute App → TestFlight & App Store → Distribute"])
        let code = body.blocks.flatMap { block in
            block.text.runs.filter { $0.inlinePresentationIntent?.contains(.code) == true }
                .map { String(block.text[$0.range].characters) }
        }
        XCTAssertEqual(code, ["Failed to Use Accounts"])
    }

    func testJSONWithSiblingFieldsIsNotLossilyUnwrapped() {
        let raw = #"{"message":"done","count":9007199254740993,"precise":0.123456789012345678901234,"empty":[],"nested":{"ok":true},"message":"again"}"#
        let body = NotificationBody(raw)
        XCTAssertTrue(body.blocks.isEmpty)
        XCTAssertEqual(body.original, raw)
        XCTAssertEqual(body.json, """
        {
          "message": "done",
          "count": 9007199254740993,
          "precise": 0.123456789012345678901234,
          "empty": [],
          "nested": {
            "ok": true
          },
          "message": "again"
        }
        """)
    }

    func testJSONFormattingPreservesEscapesAndWhitespaceInsideStrings() throws {
        let raw = #"[{"path":"C:\\tmp","text":"say \"hi\", { keep  spaces }\nnext","unicode":"\u4f60\u597d"},{}]"#
        let body = NotificationBody(raw)
        let formatted = try XCTUnwrap(body.json)
        XCTAssertTrue(formatted.contains(#""text": "say \"hi\", { keep  spaces }\nnext""#))
        XCTAssertTrue(formatted.contains(#""unicode": "\u4f60\u597d""#))
        XCTAssertEqual(try JSONSerialization.jsonObject(with: Data(formatted.utf8)) as? NSArray,
                       try JSONSerialization.jsonObject(with: Data(raw.utf8)) as? NSArray)
    }

    func testUnknownAndEmptyEnvelopesRemainJSON() {
        for raw in [#"{"suggestions":[]}"#, #"{"suggestion":""}"#, #"{"message":null}"#, #"{"other":"hello"}"#, "[]", "{}"] {
            XCTAssertNotNil(NotificationBody(raw).json, raw)
        }
    }

    func testMalformedJSONRemainsReadable() {
        let raw = #"{"suggestion":"unfinished"#
        let body = NotificationBody(raw)
        XCTAssertNil(body.json)
        XCTAssertEqual(String(body.blocks[0].text.characters), raw)
    }

    func testMarkdownBlocksListsCodeQuotesAndTables() {
        let body = NotificationBody("""
        # Result

        3. first
        4. second
           - nested

        > A quote

        ```json
        {"literal":"**not bold**"}
        ```

        | Name | State |
        | --- | --- |
        | App | Ready |

        ---
        """)
        XCTAssertEqual(body.blocks[0].heading, 1)
        XCTAssertEqual(body.blocks.compactMap(\.marker), ["3.", "4.", "•"])
        XCTAssertEqual(body.blocks.first { $0.marker == "•" }?.listDepth, 2)
        XCTAssertTrue(body.blocks.contains { $0.isQuote && String($0.text.characters) == "A quote" })
        let code = body.blocks.first { $0.isCode }
        XCTAssertEqual(code.map { String($0.text.characters) }, "{\"literal\":\"**not bold**\"}\n")
        XCTAssertEqual(body.blocks.filter { $0.tableID != nil }.count, 4)
        XCTAssertEqual(body.blocks.filter(\.isTableHeader).count, 2)
        XCTAssertTrue(body.blocks.last?.isDivider == true)
    }

    func testOnlySafeExternalLinksRemainInteractive() {
        let body = NotificationBody("[Docs](https://example.com) [local](file:///tmp/private) [command](javascript:alert)")
        let links = body.blocks.flatMap { $0.text.runs.compactMap(\.link) }
        XCTAssertEqual(links.map(\.absoluteString), ["https://example.com"])
        XCTAssertEqual(String(body.blocks[0].text.characters), "Docs local command")
    }

    func testTextEnvelopeSupportsMarkdownWithoutChangingOriginal() {
        let raw = ###"{"message":"## Done\n\n**Passed** and `checked`."}"###
        let body = NotificationBody(raw)
        XCTAssertEqual(body.original, raw)
        XCTAssertEqual(body.blocks.first?.heading, 2)
        XCTAssertEqual(String(body.blocks[1].text.characters), "Passed and checked.")
    }
}


extension NotificationBodyTests {
    func testInboxPreviewRendersInlineMarkdownAndDisablesLinksWithoutChangingDetails() throws {
        let raw = "**构建完成**，请运行 `meow doctor`，查看 [文档](https://example.com)。"
        let body = NotificationBody(raw)
        let preview = try XCTUnwrap(body.listPreview)
        XCTAssertEqual(String(preview.characters), "构建完成，请运行 meow doctor，查看 文档。")
        XCTAssertTrue(preview.runs.contains { $0.inlinePresentationIntent?.contains(.stronglyEmphasized) == true })
        XCTAssertTrue(preview.runs.contains { $0.inlinePresentationIntent?.contains(.code) == true })
        XCTAssertTrue(preview.runs.allSatisfy { $0.link == nil })
        XCTAssertTrue(body.blocks[0].text.runs.contains { $0.link?.absoluteString == "https://example.com" })
        XCTAssertEqual(body.original, raw)
    }

    func testInboxPreviewUsesReadableMarkdownBlockWithoutLayoutMarkers() throws {
        for (raw, expected) in [
            ("# **结果**\n\n正文", "结果"),
            ("- *第一项*\n- 第二项", "第一项"),
            ("> 请 **检查**", "请 检查"),
            ("---\n\n完成", "完成"),
            ("```sh\necho hello\n```\n\n**完成**", "完成"),
            (###"{"message":"**已完成**"}"###, "已完成"),
            ("  \n\n 正文  \n\n第二段", "正文"),
            (#"\*literal\* and \`literal\`"#, "*literal* and `literal`")
        ] {
            XCTAssertEqual(String(try XCTUnwrap(NotificationBody(raw).listPreview).characters), expected, raw)
        }
        XCTAssertTrue(try XCTUnwrap(NotificationBody("- *第一项*").listPreview).runs.contains {
            $0.inlinePresentationIntent?.contains(.emphasized) == true
        })
    }

    func testInboxPreviewHidesCodeAndStructuredJSONWhenNoReadableProseExists() {
        for raw in ["", "   \n", "---", "```sh\necho hello\n```", #"{"data":[1,2]}"#] {
            XCTAssertNil(NotificationBody(raw).listPreview, raw)
        }
    }

    func testInboxPreviewTruncatesRenderedCharactersWithoutSplittingEmojiOrMarkdown() throws {
        let emoji = "👨‍👩‍👧‍👦"
        let raw = "**" + String(repeating: emoji, count: 190) + "**"
        let body = NotificationBody(raw)
        let preview = try XCTUnwrap(body.listPreview)
        XCTAssertEqual(String(preview.characters), String(repeating: emoji, count: 179) + "…")
        XCTAssertEqual(preview.characters.count, 180)
        XCTAssertTrue(preview.runs.contains { $0.inlinePresentationIntent?.contains(.stronglyEmphasized) == true })
        XCTAssertEqual(String(body.blocks[0].text.characters), String(repeating: emoji, count: 190))
        XCTAssertEqual(body.original, raw)
    }
}
