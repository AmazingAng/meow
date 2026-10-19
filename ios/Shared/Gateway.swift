import Foundation

struct PushConfiguration: Decodable, Sendable {
    var mode: String
    var gatewayUrl: String?
}

struct GatewayRegistration: Decodable, Sendable {
    var id: String
    var managementToken: String
    var pushToken: String
    var bindingId: String
}

/// Only the phone retains management credentials. The server receives pushToken.
struct GatewayBinding: Codable, Equatable, Sendable {
    var gatewayURL: URL
    var serverURL: URL
    var deviceID: String
    var routeID: String
    var managementToken: String
    var pushToken: String
    var bindingID: String
}

/// Do not follow redirects with credentials or request bodies.
private final class NoRedirects: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest,
                    completionHandler: @escaping (URLRequest?) -> Void) { completionHandler(nil) }
}

enum MeowTransport {
    static let gateway = URLSession(configuration: .default, delegate: NoRedirects(), delegateQueue: nil)

    static func gatewayURL(_ raw: String) -> URL? {
        guard let c = URLComponents(string: raw), let host = c.host, !host.isEmpty,
              c.user == nil, c.password == nil, c.query == nil, c.fragment == nil,
              c.scheme == "https" || c.scheme == "http"
        else { return nil }
        return MeowShared.serverURL(from: raw)
    }
}
