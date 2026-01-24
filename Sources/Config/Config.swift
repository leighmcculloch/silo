import Foundation

/// ToolConfig represents configuration for a specific AI tool
public struct ToolConfig: Codable, Equatable {
    /// Read-only mounts specific to this tool
    public var mountsRO: [String]

    /// Read-write mounts specific to this tool
    public var mountsRW: [String]

    /// Environment variables specific to this tool
    public var env: [String]

    enum CodingKeys: String, CodingKey {
        case mountsRO = "mounts_ro"
        case mountsRW = "mounts_rw"
        case env
    }

    public init(mountsRO: [String] = [], mountsRW: [String] = [], env: [String] = []) {
        self.mountsRO = mountsRO
        self.mountsRW = mountsRW
        self.env = env
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        mountsRO = try container.decodeIfPresent([String].self, forKey: .mountsRO) ?? []
        mountsRW = try container.decodeIfPresent([String].self, forKey: .mountsRW) ?? []
        env = try container.decodeIfPresent([String].self, forKey: .env) ?? []
    }
}

/// Config represents the silo configuration
public struct Config: Codable, Equatable {
    /// Read-only directories or files to mount into the container
    public var mountsRO: [String]

    /// Read-write directories or files to mount into the container
    public var mountsRW: [String]

    /// Environment variables. Values without '=' are passed through from host.
    /// Values with '=' are set explicitly (KEY=VALUE format).
    public var env: [String]

    /// Shell commands to run inside the container before the tool
    public var prehooks: [String]

    /// Tools defines available AI tools with their configurations
    public var tools: [String: ToolConfig]

    enum CodingKeys: String, CodingKey {
        case mountsRO = "mounts_ro"
        case mountsRW = "mounts_rw"
        case env
        case prehooks
        case tools
    }

    public init(
        mountsRO: [String] = [],
        mountsRW: [String] = [],
        env: [String] = [],
        prehooks: [String] = [],
        tools: [String: ToolConfig] = [:]
    ) {
        self.mountsRO = mountsRO
        self.mountsRW = mountsRW
        self.env = env
        self.prehooks = prehooks
        self.tools = tools
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        mountsRO = try container.decodeIfPresent([String].self, forKey: .mountsRO) ?? []
        mountsRW = try container.decodeIfPresent([String].self, forKey: .mountsRW) ?? []
        env = try container.decodeIfPresent([String].self, forKey: .env) ?? []
        prehooks = try container.decodeIfPresent([String].self, forKey: .prehooks) ?? []
        tools = try container.decodeIfPresent([String: ToolConfig].self, forKey: .tools) ?? [:]
    }
}

/// SourceInfo tracks the source of configuration values
public struct SourceInfo {
    /// value -> source path
    public var mountsRO: [String: String] = [:]
    public var mountsRW: [String: String] = [:]
    public var env: [String: String] = [:]
    public var prehooks: [String: String] = [:]

    /// tool -> value -> source
    public var toolMountsRO: [String: [String: String]] = [:]
    public var toolMountsRW: [String: [String: String]] = [:]
    public var toolEnv: [String: [String: String]] = [:]

    public init() {}
}

/// ConfigPath represents a config file path with its status
public struct ConfigPath {
    public let path: String
    public let exists: Bool

    public init(path: String, exists: Bool) {
        self.path = path
        self.exists = exists
    }
}

/// Configuration management utilities
public enum ConfigManager {
    /// Get XDG config home directory
    public static func xdgConfigHome() -> String {
        if let xdg = ProcessInfo.processInfo.environment["XDG_CONFIG_HOME"], !xdg.isEmpty {
            return xdg
        }
        let home = ProcessInfo.processInfo.environment["HOME"] ?? NSHomeDirectory()
        return (home as NSString).appendingPathComponent(".config")
    }

    /// Get XDG data home directory
    public static func xdgDataHome() -> String {
        if let xdg = ProcessInfo.processInfo.environment["XDG_DATA_HOME"], !xdg.isEmpty {
            return xdg
        }
        let home = ProcessInfo.processInfo.environment["HOME"] ?? NSHomeDirectory()
        return (home as NSString).appendingPathComponent(".local/share")
    }

    /// Replace home dir with ~ in a path
    private static func tildePath(_ path: String) -> String {
        let home = ProcessInfo.processInfo.environment["HOME"] ?? NSHomeDirectory()
        if path.hasPrefix(home) {
            return "~" + String(path.dropFirst(home.count))
        }
        return path
    }

    /// Returns the default configuration
    public static func defaultConfig() -> Config {
        return Config(
            mountsRO: [],
            mountsRW: [],
            env: [],
            prehooks: [],
            tools: [
                "claude": ToolConfig(
                    mountsRW: [
                        "~/.claude.json",
                        "~/.claude"
                    ]
                ),
                "opencode": ToolConfig(
                    mountsRW: [
                        tildePath((xdgConfigHome() as NSString).appendingPathComponent("opencode")),
                        tildePath((xdgDataHome() as NSString).appendingPathComponent("opencode"))
                    ]
                ),
                "copilot": ToolConfig(
                    mountsRW: [
                        tildePath((xdgConfigHome() as NSString).appendingPathComponent(".copilot"))
                    ],
                    env: [
                        "COPILOT_GITHUB_TOKEN"
                    ]
                )
            ]
        )
    }

    /// Strip JSONC comments from JSON data
    private static func stripJSONCComments(_ data: Data) -> Data {
        guard let string = String(data: data, encoding: .utf8) else {
            return data
        }

        var result = ""
        var i = string.startIndex
        var inString = false
        var escaped = false

        while i < string.endIndex {
            let char = string[i]

            if inString {
                result.append(char)
                if char == "\\" && !escaped {
                    escaped = true
                } else if char == "\"" && !escaped {
                    inString = false
                } else {
                    escaped = false
                }
            } else {
                // Check for single-line comment
                if char == "/" {
                    let next = string.index(after: i)
                    if next < string.endIndex {
                        if string[next] == "/" {
                            // Single-line comment - skip to end of line
                            while i < string.endIndex && string[i] != "\n" {
                                i = string.index(after: i)
                            }
                            if i < string.endIndex {
                                result.append("\n")
                            }
                            continue
                        } else if string[next] == "*" {
                            // Multi-line comment - skip to */
                            i = string.index(after: next)
                            while i < string.endIndex {
                                if string[i] == "*" {
                                    let afterStar = string.index(after: i)
                                    if afterStar < string.endIndex && string[afterStar] == "/" {
                                        i = string.index(after: afterStar)
                                        break
                                    }
                                }
                                i = string.index(after: i)
                            }
                            continue
                        }
                    }
                }

                if char == "\"" {
                    inString = true
                }
                result.append(char)
            }
            i = string.index(after: i)
        }

        return result.data(using: .utf8) ?? data
    }

    /// Load configuration from the given path (supports JSONC with comments)
    public static func load(from path: String) throws -> Config {
        let data = try Data(contentsOf: URL(fileURLWithPath: path))
        let jsonData = stripJSONCComments(data)
        let decoder = JSONDecoder()
        return try decoder.decode(Config.self, from: jsonData)
    }

    /// Merge two configs, with the overlay taking precedence for arrays (append) and maps (merge)
    public static func merge(base: Config, overlay: Config) -> Config {
        var result = base

        // Append arrays
        result.mountsRO = base.mountsRO + overlay.mountsRO
        result.mountsRW = base.mountsRW + overlay.mountsRW
        result.env = base.env + overlay.env
        result.prehooks = base.prehooks + overlay.prehooks

        // Merge tools map
        for (name, tool) in overlay.tools {
            if var existing = result.tools[name] {
                existing.mountsRO = existing.mountsRO + tool.mountsRO
                existing.mountsRW = existing.mountsRW + tool.mountsRW
                existing.env = existing.env + tool.env
                result.tools[name] = existing
            } else {
                result.tools[name] = tool
            }
        }

        return result
    }

    /// Get all config paths that would be checked/loaded
    public static func getConfigPaths() -> [ConfigPath] {
        var paths: [ConfigPath] = []

        // Global config
        let globalConfigPath = (xdgConfigHome() as NSString)
            .appendingPathComponent("silo")
            .appending("/silo.jsonc")
        let globalExists = FileManager.default.fileExists(atPath: globalConfigPath)
        paths.append(ConfigPath(path: globalConfigPath, exists: globalExists))

        // Find all config files from root to current directory
        guard let cwd = FileManager.default.currentDirectoryPath as String? else {
            return paths
        }

        var localPaths: [ConfigPath] = []
        var dir = cwd

        while true {
            let configPath = (dir as NSString).appendingPathComponent("silo.jsonc")
            let exists = FileManager.default.fileExists(atPath: configPath)
            localPaths.insert(ConfigPath(path: configPath, exists: exists), at: 0)

            let parent = (dir as NSString).deletingLastPathComponent
            if parent == dir {
                break
            }
            dir = parent
        }

        paths.append(contentsOf: localPaths)
        return paths
    }

    /// Track configuration sources for values
    private static func trackConfigSources(_ config: Config, source: String, info: inout SourceInfo) {
        for v in config.mountsRO {
            info.mountsRO[v] = source
        }
        for v in config.mountsRW {
            info.mountsRW[v] = source
        }
        for v in config.env {
            info.env[v] = source
        }
        for v in config.prehooks {
            info.prehooks[v] = source
        }
        for (toolName, toolConfig) in config.tools {
            if info.toolMountsRO[toolName] == nil {
                info.toolMountsRO[toolName] = [:]
            }
            if info.toolMountsRW[toolName] == nil {
                info.toolMountsRW[toolName] = [:]
            }
            if info.toolEnv[toolName] == nil {
                info.toolEnv[toolName] = [:]
            }
            for v in toolConfig.mountsRO {
                info.toolMountsRO[toolName]?[v] = source
            }
            for v in toolConfig.mountsRW {
                info.toolMountsRW[toolName]?[v] = source
            }
            for v in toolConfig.env {
                info.toolEnv[toolName]?[v] = source
            }
        }
    }

    /// Load and merge all configuration files
    public static func loadAll() -> Config {
        let (config, _) = loadAllWithSources()
        return config
    }

    /// Load and merge all configs, tracking the source of each value
    public static func loadAllWithSources() -> (Config, SourceInfo) {
        var config = defaultConfig()
        var sources = SourceInfo()

        // Track defaults
        trackConfigSources(config, source: "default", info: &sources)

        // Load from XDG config home
        let globalConfigPath = (xdgConfigHome() as NSString)
            .appendingPathComponent("silo")
            .appending("/silo.jsonc")
        if let globalConfig = try? load(from: globalConfigPath) {
            trackConfigSources(globalConfig, source: globalConfigPath, info: &sources)
            config = merge(base: config, overlay: globalConfig)
        }

        // Find all config files from root to current directory
        let cwd = FileManager.default.currentDirectoryPath

        var configPaths: [String] = []
        var dir = cwd

        while true {
            let configPath = (dir as NSString).appendingPathComponent("silo.jsonc")
            if FileManager.default.fileExists(atPath: configPath) {
                configPaths.insert(configPath, at: 0)
            }

            let parent = (dir as NSString).deletingLastPathComponent
            if parent == dir {
                break
            }
            dir = parent
        }

        // Load and merge configs from parent to child (child overrides parent)
        for path in configPaths {
            if let localConfig = try? load(from: path) {
                trackConfigSources(localConfig, source: path, info: &sources)
                config = merge(base: config, overlay: localConfig)
            }
        }

        return (config, sources)
    }
}
