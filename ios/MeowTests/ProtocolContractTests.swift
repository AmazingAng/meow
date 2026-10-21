import XCTest
@testable import Meow

final class ProtocolContractTests: XCTestCase {
    private struct Fixture: Decodable {
        struct Item: Decodable { let name: String; let notification: MeowNotification }
        let records: [Item]
    }

    func testSharedWireRecordsDecodeAndSurviveLocalStorageEncoding() throws {
        #if SWIFT_PACKAGE
        let bundle = Bundle.module
        #else
        let bundle = Bundle(for: Self.self)
        #endif
        let url = try XCTUnwrap(bundle.url(forResource: "notifications", withExtension: "json"))
        let fixture = try MeowJSON.decoder.decode(Fixture.self, from: Data(contentsOf: url))
        XCTAssertEqual(fixture.records.count, 3)
        for item in fixture.records {
            let roundTrip = try JSONDecoder().decode(MeowNotification.self, from: JSONEncoder().encode(item.notification))
            XCTAssertEqual(roundTrip, item.notification, item.name)
        }
        XCTAssertNil(fixture.records[0].notification.context)
        XCTAssertEqual(fixture.records[1].notification.context?.session, "部署审查")
        XCTAssertEqual(fixture.records[1].notification.context?.sessionId, "s1")
        XCTAssertEqual(fixture.records[2].notification.question?.answer, "approve")
        XCTAssertEqual(fixture.records[2].notification.type, .question)
    }
}
