import SwiftUI

enum SetupHelpTopic: String, Identifiable {
    case server, cli, scripts
    var title: LocalizedStringKey {
        switch self {
        case .server: "Server address help"
        case .cli: "Install the CLI"
        case .scripts: "Script integration"
        }
    }
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
                        Text("The official Meow server is ready to use: https://api.meow.blue")
                            .font(.headline)
                        Text("For a self-hosted server, enter the full http:// or https:// address from its operator. If you host Meow on your computer, use its LAN address on the same Wi-Fi. 127.0.0.1 on your iPhone points to the iPhone itself.")
                        Text("The server address is different from your Device Key. Meow creates that key after you connect.")
                            .foregroundStyle(.secondary)
                        Text("Notification content passes through and is stored on your chosen server. You can use a self-hosted server to manage that data yourself.")
                            .foregroundStyle(.secondary)
                        Link("Server deployment guide", destination: URL(string: "https://github.com/AmazingAng/meow/blob/main/deploy/README.md")!)
                            .frame(minHeight: 44)
                    } else if topic == .scripts {
                        Text("Send notifications without the CLI").font(.headline)
                        Text("On your computer, run the first command, paste the key copied with Copy Device Key, and press Return. The key is hidden while you type. Then run the curl command. These examples work in bash and zsh.")
                        CommandBlock(title: "1. Read your Device Key", command: AppModel.scriptKeyInputCommand, copyTitle: "Copy key input command")
                        Text("The key is entered separately so it is not included in the copied command or shell history. For automated scripts, load MEOW_KEY from your secret manager.")
                            .font(.footnote).foregroundStyle(.secondary)
                        CommandBlock(title: "2. Send a test notification", command: model.scriptNotificationCommand, copyTitle: "Copy curl command")
                        Link("HTTP API and script examples", destination: URL(string: "https://github.com/AmazingAng/meow#http-api")!)
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
            .navigationTitle(topic.title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } }
            }
        }
    }
}
