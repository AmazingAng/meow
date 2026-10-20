import SwiftUI

enum SetupHelpTopic: String, Identifiable {
    case server, cli
    var id: Self { self }
}

struct SetupHelpView: View {
    let topic: SetupHelpTopic
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    if topic == .server {
                        Text("Use the address from the person running your Meow server.")
                            .font(.headline)
                        Text("Enter the full http:// or https:// address. If you host Meow on your computer, use its LAN address while your iPhone is on the same Wi-Fi. 127.0.0.1 on your iPhone points to the iPhone itself.")
                        Text("The server address is different from your Device Key. Meow creates that key after you connect.")
                            .foregroundStyle(.secondary)
                        Link("Server deployment guide", destination: URL(string: "https://github.com/AmazingAng/meow/blob/main/deploy/README.md")!)
                            .frame(minHeight: 44)
                    } else {
                        Text("Install Meow on your computer").font(.headline)
                        Text("Download a ready-to-run CLI. No Go or GitHub account is needed.")
                        CommandBlock(title: "macOS · Homebrew", command: AppModel.cliHomebrewCommand, copyTitle: "Copy install command")
                        CommandBlock(title: "macOS / Linux · Direct install", command: model.installAndSetupCommand, copyTitle: "Copy install and setup command")
                        Text("Direct installation saves Meow in ~/.local/bin and checks its SHA-256 checksum. It starts setup for this server immediately; paste your Device Key when asked.")
                            .font(.footnote).foregroundStyle(.secondary)
                        CommandBlock(title: "After Homebrew, or if already installed", command: model.setupCommand, copyTitle: "Copy setup command")
                        Link("Downloads and installation guide", destination: URL(string: "https://github.com/AmazingAng/meow#installation")!)
                            .frame(minHeight: 44)
                        CommandBlock(title: "Verify installation", command: "meow --version")
                        Text("Setup sends a test notification to this iPhone. Your Device Key is entered separately and is not included in the copied setup command.")
                    }
                }
                .frame(maxWidth: 640, alignment: .leading)
                .padding(24)
                .frame(maxWidth: .infinity)
            }
            .navigationTitle(topic == .server ? LocalizedStringKey("Server address help") : LocalizedStringKey("Install the CLI"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } }
            }
        }
    }
}
