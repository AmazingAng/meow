import Foundation

/// Finish the Objective-C notification callback on the UI executor. UIKit may
/// perform scene restoration synchronously inside the system completion block.
enum NotificationCallback {
    static func run<Value>(_ operation: @escaping @MainActor () async -> Value,
                           completion: @escaping (Value) -> Void) {
        Task { @MainActor in
            let value = await operation()
            completion(value)
        }
    }
}
