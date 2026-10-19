import SwiftUI

enum SetupHelpTopic: String, Identifiable {
    case server, cli
    var id: Self { self }
}

struct SetupHelpView: View {
    let topic: SetupHelpTopic
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
                        Text("Build the CLI from source on macOS or Linux. Install Go and GitHub CLI first, then sign in to GitHub with an account that can access the repository.")
                        CommandBlock(title: "Build and install", command: """
                        gh auth login
                        gh repo clone AmazingAng/meow
                        cd meow
                        mkdir -p "$HOME/.local/bin"
                        go build -o "$HOME/.local/bin/meow" ./cli/cmd/meow
                        export PATH="$HOME/.local/bin:$PATH"
                        """)
                        Link("CLI source and installation guide", destination: URL(string: "https://github.com/AmazingAng/meow#agent-integrations")!)
                            .frame(minHeight: 44)
                        Text("Repository access is required. If you don't have access, ask the person running your server for the CLI.")
                            .font(.footnote).foregroundStyle(.secondary)
                        CommandBlock(title: "Verify installation", command: "meow --version")
                        Text("When the version appears, return to setup and run the connection command. Keep your Device Key ready to paste when asked.")
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
