import ArgumentParser
import Foundation
import CLI
import Config
import Docker

let version = "dev"

let sampleConfig = """
{
  // Read-only directories or files to mount into the container
  "mounts_ro": [],
  // Read-write directories or files to mount into the container
  "mounts_rw": [],
  // Environment variables: names without '=' pass through from host,
  // names with '=' set explicitly (e.g., "FOO=bar")
  "env": [],
  // Shell commands to run inside the container before the tool
  "prehooks": [],
  // Tool-specific configuration (merged with global config above)
  // Example: "tools": { "claude": { "env": ["CLAUDE_SPECIFIC_VAR"] } }
  "tools": {}
}
"""

/// Expand ~ to the user's home directory
func expandPath(_ path: String) -> String {
    if path.hasPrefix("~/") {
        let home = ProcessInfo.processInfo.environment["HOME"] ?? NSHomeDirectory()
        return (home as NSString).appendingPathComponent(String(path.dropFirst(2)))
    }
    if path == "~" {
        return ProcessInfo.processInfo.environment["HOME"] ?? NSHomeDirectory()
    }
    return path
}

/// Replace home dir with ~ in paths
func tildePath(_ path: String) -> String {
    let home = ProcessInfo.processInfo.environment["HOME"] ?? NSHomeDirectory()
    if path.hasPrefix(home) {
        return "~" + String(path.dropFirst(home.count))
    }
    return path
}

// MARK: - Main Command

@main
struct Silo: ParsableCommand {
    static var configuration = CommandConfiguration(
        commandName: "silo",
        abstract: "Run AI coding tools in isolated Docker containers",
        discussion: """
        \(CLI.title("""
          ███████╗██╗██╗      ██████╗
          ██╔════╝██║██║     ██╔═══██╗
          ███████╗██║██║     ██║   ██║
          ╚════██║██║██║     ██║   ██║
          ███████║██║███████╗╚██████╔╝
          ╚══════╝╚═╝╚══════╝ ╚═════╝
        """))

        Run AI coding assistants (Claude Code, OpenCode, Copilot) in isolated
        Docker containers with proper security sandboxing.

        The container is configured with:
          • Your current directory mounted as the working directory
          • Git identity from your host machine
          • Tool-specific configuration directories
          • API keys from configured key files

        Configuration is loaded from (in order, merged):
          1. ~/.config/silo/config.json (global)
          2. .silo.json files from root to current directory (local)
        """,
        version: "silo version \(version)",
        subcommands: [ConfigCommand.self],
        defaultSubcommand: nil
    )

    @Argument(help: "The AI tool to run (opencode, claude, copilot)")
    var tool: String?

    @Argument(parsing: .captureForPassthrough, help: "Arguments to pass to the tool")
    var toolArgs: [String] = []

    mutating func run() throws {
        // Load configuration
        var cfg: Config.Config
        do {
            cfg = ConfigManager.loadAll()
        } catch {
            CLI.logWarning("Failed to load config: %@", "\(error)")
            cfg = ConfigManager.defaultConfig()
        }

        // Determine tool
        var selectedTool: String
        if let t = tool {
            selectedTool = t
        } else {
            // Interactive selection
            selectedTool = try selectTool()
        }

        // Validate tool
        guard availableTools.contains(selectedTool) else {
            throw ValidationError("Invalid tool: \(selectedTool) (valid tools: \(availableTools.joined(separator: ", ")))")
        }

        // Run the tool
        try runTool(selectedTool, toolArgs: toolArgs, config: cfg)
    }
}

/// Interactive tool selection
func selectTool() throws -> String {
    print("Select AI Tool:")
    print("Choose which AI coding assistant to run\n")

    for (i, tool) in availableTools.enumerated() {
        print("  [\(i + 1)] \(toolDescription(tool))")
    }

    print("\nEnter selection (1-\(availableTools.count)): ", terminator: "")
    fflush(stdout)

    guard let line = readLine(), let selection = Int(line),
          selection >= 1, selection <= availableTools.count else {
        throw ValidationError("Invalid selection")
    }

    return availableTools[selection - 1]
}

/// Run the selected tool
func runTool(_ tool: String, toolArgs: [String], config: Config.Config) throws {
    // Create Docker client
    CLI.log("Connecting to Docker...")
    let dockerClient: DockerClient
    do {
        dockerClient = try DockerClient()
    } catch {
        throw ValidationError("Failed to connect to Docker: \(error.localizedDescription)")
    }

    // Get current user info
    let home = ProcessInfo.processInfo.environment["HOME"] ?? NSHomeDirectory()
    let user = ProcessInfo.processInfo.environment["USER"] ?? "user"
    let uid = getuid()

    // Build the image
    CLI.log("Preparing image for %@...", tool)
    do {
        _ = try dockerClient.build(BuildOptions(
            dockerfile: dockerfile(),
            target: tool,
            buildArgs: [
                "HOME": home,
                "USER": user,
                "UID": "\(uid)"
            ]
        ))
    } catch {
        throw ValidationError("Failed to build image: \(error.localizedDescription)")
    }
    CLI.logSuccess("Image ready")

    // Collect mounts
    let cwd = FileManager.default.currentDirectoryPath
    var mountsRW = [cwd]
    var mountsRO: [String] = []

    // Add tool-specific mounts
    if let toolCfg = config.tools[tool] {
        for m in toolCfg.mountsRO {
            mountsRO.append(expandPath(m))
        }
        for m in toolCfg.mountsRW {
            mountsRW.append(expandPath(m))
        }
    }

    // Add global config mounts
    for m in config.mountsRO {
        mountsRO.append(expandPath(m))
    }
    for m in config.mountsRW {
        mountsRW.append(expandPath(m))
    }

    // Add git worktree roots (read-write for git operations)
    let worktreeRoots = getGitWorktreeRoots(dir: cwd)
    mountsRW.append(contentsOf: worktreeRoots)

    // Collect environment variables
    var envVars: [String] = []

    // Get git identity
    let (gitName, gitEmail) = getGitIdentity()
    if !gitName.isEmpty {
        envVars.append("GIT_AUTHOR_NAME=\(gitName)")
        envVars.append("GIT_COMMITTER_NAME=\(gitName)")
        CLI.log("Git identity: %@ <%@>", gitName, gitEmail)
    }
    if !gitEmail.isEmpty {
        envVars.append("GIT_AUTHOR_EMAIL=\(gitEmail)")
        envVars.append("GIT_COMMITTER_EMAIL=\(gitEmail)")
    }

    // Process env vars (passthrough if no '=', explicit if has '=')
    for e in config.env {
        if e.contains("=") {
            envVars.append(e)
        } else if let val = ProcessInfo.processInfo.environment[e] {
            envVars.append("\(e)=\(val)")
        }
    }

    // Tool-specific env vars
    if let toolCfg = config.tools[tool] {
        for e in toolCfg.env {
            if e.contains("=") {
                envVars.append(e)
            } else if let val = ProcessInfo.processInfo.environment[e] {
                envVars.append("\(e)=\(val)")
            }
        }
    }

    // Generate container name
    var baseName = (cwd as NSString).lastPathComponent
    baseName = baseName.replacingOccurrences(of: ".", with: "")
    let containerName = "\(baseName)-\(String(format: "%02d", Int.random(in: 0..<100)))"

    // Log mounts
    var seen = Set<String>()
    if !mountsRO.isEmpty {
        CLI.log("Mounts (read-only):")
        for m in mountsRO {
            guard FileManager.default.fileExists(atPath: m) else { continue }
            guard !seen.contains(m) else { continue }
            seen.insert(m)
            CLI.logBullet("%@", m)
        }
    }
    CLI.log("Mounts (read-write):")
    for m in mountsRW {
        guard FileManager.default.fileExists(atPath: m) else { continue }
        guard !seen.contains(m) else { continue }
        seen.insert(m)
        CLI.logBullet("%@", m)
    }

    CLI.log("Container name: %@", containerName)
    CLI.log("Running %@...", tool)

    // Define tool-specific commands
    let toolCommands: [String: [String]] = [
        "claude": ["claude", "--mcp-config=\(home)/.claude/mcp.json", "--dangerously-skip-permissions"],
        "opencode": ["opencode"],
        "copilot": ["copilot", "--allow-all"]
    ]

    // Run the container
    try dockerClient.run(RunOptions(
        image: tool,
        name: containerName,
        workDir: cwd,
        mountsRO: mountsRO,
        mountsRW: mountsRW,
        env: envVars,
        command: toolCommands[tool] ?? [],
        args: toolArgs,
        prehooks: config.prehooks,
        tty: true,
        removeOnExit: true,
        securityOptions: ["no-new-privileges:true"]
    ))
}

// MARK: - Config Commands

struct ConfigCommand: ParsableCommand {
    static var configuration = CommandConfiguration(
        commandName: "config",
        abstract: "Configuration management commands",
        subcommands: [
            ConfigShow.self,
            ConfigPaths.self,
            ConfigEdit.self,
            ConfigDefault.self,
            ConfigInit.self
        ]
    )
}

struct ConfigShow: ParsableCommand {
    static var configuration = CommandConfiguration(
        commandName: "show",
        abstract: "Show the current merged configuration"
    )

    func run() throws {
        let (cfg, sources) = ConfigManager.loadAllWithSources()

        // Check if stdout is a TTY for color output
        let isTTY = isTerminal(FileHandle.standardOutput)

        // Color functions
        func key(_ k: String) -> String {
            if isTTY {
                return "\(ANSIColor.configKey.rawValue)\"\(k)\"\(ANSIColor.reset.rawValue)"
            }
            return "\"\(k)\""
        }

        func str(_ s: String) -> String {
            if isTTY {
                return "\(ANSIColor.configString.rawValue)\"\(s)\"\(ANSIColor.reset.rawValue)"
            }
            return "\"\(s)\""
        }

        func comment(_ c: String) -> String {
            if isTTY {
                return "\(ANSIColor.configComment.rawValue)// \(tildePath(c))\(ANSIColor.reset.rawValue)"
            }
            return "// \(tildePath(c))"
        }

        // Output JSONC with source comments
        print("{")

        // MountsRO
        print("  \(key("mounts_ro")): [")
        for (i, v) in cfg.mountsRO.enumerated() {
            let comma = i == cfg.mountsRO.count - 1 ? "" : ","
            let source = sources.mountsRO[v] ?? "unknown"
            print("    \(str(v))\(comma) \(comment(source))")
        }
        print("  ],")

        // MountsRW
        print("  \(key("mounts_rw")): [")
        for (i, v) in cfg.mountsRW.enumerated() {
            let comma = i == cfg.mountsRW.count - 1 ? "" : ","
            let source = sources.mountsRW[v] ?? "unknown"
            print("    \(str(v))\(comma) \(comment(source))")
        }
        print("  ],")

        // Env
        print("  \(key("env")): [")
        for (i, v) in cfg.env.enumerated() {
            let comma = i == cfg.env.count - 1 ? "" : ","
            let source = sources.env[v] ?? "unknown"
            print("    \(str(v))\(comma) \(comment(source))")
        }
        print("  ],")

        // Prehooks
        print("  \(key("prehooks")): [")
        for (i, v) in cfg.prehooks.enumerated() {
            let comma = i == cfg.prehooks.count - 1 ? "" : ","
            let source = sources.prehooks[v] ?? "unknown"
            print("    \(str(v))\(comma) \(comment(source))")
        }
        print("  ],")

        // Tools
        print("  \(key("tools")): {")
        let toolNames = cfg.tools.keys.sorted()

        for (ti, toolName) in toolNames.enumerated() {
            guard let toolCfg = cfg.tools[toolName] else { continue }
            print("    \(key(toolName)): {")

            // Tool mounts_ro
            print("      \(key("mounts_ro")): [")
            for (i, v) in toolCfg.mountsRO.enumerated() {
                let comma = i == toolCfg.mountsRO.count - 1 ? "" : ","
                let source = sources.toolMountsRO[toolName]?[v] ?? "unknown"
                print("        \(str(v))\(comma) \(comment(source))")
            }
            print("      ],")

            // Tool mounts_rw
            print("      \(key("mounts_rw")): [")
            for (i, v) in toolCfg.mountsRW.enumerated() {
                let comma = i == toolCfg.mountsRW.count - 1 ? "" : ","
                let source = sources.toolMountsRW[toolName]?[v] ?? "unknown"
                print("        \(str(v))\(comma) \(comment(source))")
            }
            print("      ],")

            // Tool env
            print("      \(key("env")): [")
            for (i, v) in toolCfg.env.enumerated() {
                let comma = i == toolCfg.env.count - 1 ? "" : ","
                let source = sources.toolEnv[toolName]?[v] ?? "unknown"
                print("        \(str(v))\(comma) \(comment(source))")
            }
            print("      ]")

            let toolComma = ti == toolNames.count - 1 ? "" : ","
            print("    }\(toolComma)")
        }
        print("  }")

        print("}")
    }
}

struct ConfigPaths: ParsableCommand {
    static var configuration = CommandConfiguration(
        commandName: "paths",
        abstract: "Show all config file paths being merged"
    )

    func run() throws {
        let paths = ConfigManager.getConfigPaths()

        for p in paths where p.exists {
            print(p.path)
        }
    }
}

struct ConfigEdit: ParsableCommand {
    static var configuration = CommandConfiguration(
        commandName: "edit",
        abstract: "Edit a config file in your editor"
    )

    func run() throws {
        let paths = ConfigManager.getConfigPaths()

        // Build options for the selector
        var options: [(label: String, path: String)] = []
        for (i, p) in paths.enumerated() {
            let isGlobal = i == 0
            if !isGlobal && !p.exists {
                continue
            }
            var label = p.path
            if !p.exists {
                label += " (new)"
            }
            options.append((label: label, path: p.path))
        }

        print("Select Config to Edit:")
        print("Configs are merged in order shown (later overrides earlier)\n")

        for (i, opt) in options.enumerated() {
            print("  [\(i + 1)] \(opt.label)")
        }

        print("\nEnter selection (1-\(options.count)): ", terminator: "")
        fflush(stdout)

        guard let line = readLine(), let selection = Int(line),
              selection >= 1, selection <= options.count else {
            throw ValidationError("Selection cancelled")
        }

        let selectedPath = options[selection - 1].path

        // Get editor from environment
        var editor = ProcessInfo.processInfo.environment["EDITOR"]
            ?? ProcessInfo.processInfo.environment["VISUAL"]
            ?? "vi"

        // Ensure parent directory exists
        let dir = (selectedPath as NSString).deletingLastPathComponent
        try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)

        // If file doesn't exist, pre-fill with template
        if !FileManager.default.fileExists(atPath: selectedPath) {
            try sampleConfig.write(toFile: selectedPath, atomically: true, encoding: .utf8)
        }

        // Open editor
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/env")
        process.arguments = [editor, selectedPath]
        process.standardInput = FileHandle.standardInput
        process.standardOutput = FileHandle.standardOutput
        process.standardError = FileHandle.standardError

        try process.run()
        process.waitUntilExit()

        if process.terminationStatus != 0 {
            throw ValidationError("Editor failed")
        }
    }
}

struct ConfigDefault: ParsableCommand {
    static var configuration = CommandConfiguration(
        commandName: "default",
        abstract: "Show the default configuration"
    )

    func run() throws {
        let cfg = ConfigManager.defaultConfig()

        // Output as JSON
        print("{")

        // MountsRO
        print("  \"mounts_ro\": [")
        for (i, v) in cfg.mountsRO.enumerated() {
            let comma = i == cfg.mountsRO.count - 1 ? "" : ","
            print("    \"\(v)\"\(comma)")
        }
        print("  ],")

        // MountsRW
        print("  \"mounts_rw\": [")
        for (i, v) in cfg.mountsRW.enumerated() {
            let comma = i == cfg.mountsRW.count - 1 ? "" : ","
            print("    \"\(v)\"\(comma)")
        }
        print("  ],")

        // Env
        print("  \"env\": [")
        for (i, v) in cfg.env.enumerated() {
            let comma = i == cfg.env.count - 1 ? "" : ","
            print("    \"\(v)\"\(comma)")
        }
        print("  ],")

        // Prehooks
        print("  \"prehooks\": [")
        for (i, v) in cfg.prehooks.enumerated() {
            let comma = i == cfg.prehooks.count - 1 ? "" : ","
            print("    \"\(v)\"\(comma)")
        }
        print("  ],")

        // Tools
        print("  \"tools\": {")
        let toolNames = cfg.tools.keys.sorted()

        for (ti, toolName) in toolNames.enumerated() {
            guard let toolCfg = cfg.tools[toolName] else { continue }
            print("    \"\(toolName)\": {")

            print("      \"mounts_ro\": [")
            for (i, v) in toolCfg.mountsRO.enumerated() {
                let comma = i == toolCfg.mountsRO.count - 1 ? "" : ","
                print("        \"\(v)\"\(comma)")
            }
            print("      ],")

            print("      \"mounts_rw\": [")
            for (i, v) in toolCfg.mountsRW.enumerated() {
                let comma = i == toolCfg.mountsRW.count - 1 ? "" : ","
                print("        \"\(v)\"\(comma)")
            }
            print("      ],")

            print("      \"env\": [")
            for (i, v) in toolCfg.env.enumerated() {
                let comma = i == toolCfg.env.count - 1 ? "" : ","
                print("        \"\(v)\"\(comma)")
            }
            print("      ]")

            let toolComma = ti == toolNames.count - 1 ? "" : ","
            print("    }\(toolComma)")
        }
        print("  }")

        print("}")
    }
}

struct ConfigInit: ParsableCommand {
    static var configuration = CommandConfiguration(
        commandName: "init",
        abstract: "Create a sample configuration file",
        discussion: """
        Create a sample silo configuration file.

        By default, an interactive prompt lets you choose between local and global config.
        Use --local or --global to skip the prompt.
        """
    )

    @Flag(name: .shortAndLong, help: "Create global config (~/.config/silo/silo.jsonc)")
    var global: Bool = false

    @Flag(name: .shortAndLong, help: "Create local config (silo.jsonc)")
    var local: Bool = false

    func validate() throws {
        if global && local {
            throw ValidationError("Cannot specify both --global and --local")
        }
    }

    func run() throws {
        var configType: String

        if global {
            configType = "global"
        } else if local {
            configType = "local"
        } else {
            // Interactive selection
            print("Create Configuration:")
            print("Choose which configuration file to create\n")
            print("  [1] Local (silo.jsonc in current directory)")
            print("  [2] Global (~/.config/silo/silo.jsonc)")
            print("\nEnter selection (1-2): ", terminator: "")
            fflush(stdout)

            guard let line = readLine(), let selection = Int(line),
                  selection >= 1, selection <= 2 else {
                throw ValidationError("Selection cancelled")
            }

            configType = selection == 1 ? "local" : "global"
        }

        var configPath: String
        if configType == "global" {
            let configDir = (ConfigManager.xdgConfigHome() as NSString).appendingPathComponent("silo")
            try FileManager.default.createDirectory(atPath: configDir, withIntermediateDirectories: true)
            configPath = (configDir as NSString).appendingPathComponent("silo.jsonc")
        } else {
            configPath = "silo.jsonc"
        }

        if FileManager.default.fileExists(atPath: configPath) {
            throw ValidationError("Config file already exists: \(configPath)")
        }

        try sampleConfig.write(toFile: configPath, atomically: true, encoding: .utf8)
        CLI.logSuccess("Created %@", configPath)
    }
}
